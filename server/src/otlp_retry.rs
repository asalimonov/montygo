//! OTLP exporters that retry a failed export and report it. A batch that the
//! SDK exporter cannot deliver is otherwise dropped after one attempt, and its
//! failure goes to the tracing dispatcher the pool telemetry owns.

use std::{
    fmt::Debug,
    future::Future,
    sync::{
        Mutex,
        atomic::{AtomicBool, Ordering},
    },
    time::{Duration, Instant},
};

use opentelemetry::InstrumentationScope;
use opentelemetry_sdk::{
    Resource,
    error::OTelSdkResult,
    logs::{LogBatch, LogExporter, SdkLogRecord},
    trace::{SpanData, SpanExporter},
};

use crate::logline;

/// Delays before each retry. A freshly started container can lack a route to
/// the collector for several seconds.
const RETRY_DELAYS: [Duration; 5] = [
    Duration::from_millis(500),
    Duration::from_secs(1),
    Duration::from_secs(2),
    Duration::from_secs(4),
    Duration::from_secs(4),
];
const FAILURE_LOG_INTERVAL: Duration = Duration::from_secs(60);

#[derive(Debug)]
pub struct Retrying<E> {
    inner: E,
    signal: &'static str,
    last_failure_log: Mutex<Option<Instant>>,
    established: AtomicBool,
}

impl<E> Retrying<E> {
    pub fn new(inner: E, signal: &'static str) -> Self {
        Self {
            inner,
            signal,
            last_failure_log: Mutex::new(None),
            established: AtomicBool::new(false),
        }
    }

    /// Runs attempt until it succeeds or the delays are spent; failures are
    /// logged once per interval, the final one always.
    async fn attempts<F, Fut>(&self, mut attempt: F) -> OTelSdkResult
    where
        F: FnMut() -> Fut,
        Fut: Future<Output = OTelSdkResult>,
    {
        let mut failures = 0;
        loop {
            let err = match attempt().await {
                Ok(()) => {
                    if !self.established.swap(true, Ordering::Relaxed) {
                        logline::info("otlp_export_established", &[("signal", &self.signal), ("attempts", &(failures + 1))]);
                    }
                    return Ok(());
                }
                Err(err) => err,
            };
            if failures == RETRY_DELAYS.len() {
                logline::warn(
                    "otlp_export_dropped",
                    &[("signal", &self.signal), ("attempts", &(failures + 1)), ("error", &err)],
                );
                return Err(err);
            }
            self.note_failure(&err, failures + 1);
            // The batch processors call export from their own threads.
            std::thread::sleep(RETRY_DELAYS[failures]);
            failures += 1;
        }
    }

    fn note_failure(&self, err: &dyn Debug, attempt: usize) {
        let mut last = self.last_failure_log.lock().unwrap_or_else(std::sync::PoisonError::into_inner);
        if last.is_none_or(|at| at.elapsed() >= FAILURE_LOG_INTERVAL) {
            *last = Some(Instant::now());
            logline::warn(
                "otlp_export_failed",
                &[("signal", &self.signal), ("attempt", &attempt), ("error", &format!("{err:?}"))],
            );
        }
    }
}

impl<E: SpanExporter> SpanExporter for Retrying<E> {
    async fn export(&self, batch: Vec<SpanData>) -> OTelSdkResult {
        self.attempts(|| self.inner.export(batch.clone())).await
    }

    fn shutdown_with_timeout(&self, timeout: Duration) -> OTelSdkResult {
        self.inner.shutdown_with_timeout(timeout)
    }

    fn force_flush(&self) -> OTelSdkResult {
        self.inner.force_flush()
    }

    fn set_resource(&mut self, resource: &Resource) {
        self.inner.set_resource(resource);
    }
}

impl<E: LogExporter> LogExporter for Retrying<E> {
    async fn export(&self, batch: LogBatch<'_>) -> OTelSdkResult {
        let records: Vec<(&SdkLogRecord, &InstrumentationScope)> = batch.iter().collect();
        self.attempts(|| self.inner.export(LogBatch::new(&records))).await
    }

    fn shutdown_with_timeout(&self, timeout: Duration) -> OTelSdkResult {
        self.inner.shutdown_with_timeout(timeout)
    }

    fn event_enabled(&self, level: opentelemetry::logs::Severity, target: &str, name: Option<&str>) -> bool {
        self.inner.event_enabled(level, target, name)
    }

    fn set_resource(&mut self, resource: &Resource) {
        self.inner.set_resource(resource);
    }
}
