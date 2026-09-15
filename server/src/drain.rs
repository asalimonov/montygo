use std::{
    sync::{Arc, OnceLock},
    time::Duration,
};

use tokio::{
    signal::unix::{SignalKind, signal},
    time::{Instant, sleep},
};
use tokio_util::sync::CancellationToken;

use crate::{logline, metrics::Metrics};

#[derive(Clone, Debug, Default)]
pub struct DrainSignals {
    /// Cancelled by the first SIGTERM or SIGINT.
    pub soft: CancellationToken,
    /// Cancelled by a second signal or when the drain grace expires.
    pub hard: CancellationToken,
    pub grace_deadline: Arc<OnceLock<Instant>>,
}

impl DrainSignals {
    pub fn new() -> Self {
        Self::default()
    }

    /// Starts the drain as the first signal would.
    pub fn begin(&self, grace: Duration, metrics: &Metrics) {
        if self.soft.is_cancelled() {
            self.hard.cancel();
            return;
        }
        let _ = self.grace_deadline.set(Instant::now() + grace);
        metrics.draining.set(1);
        logline::info("drain_started", &[("grace_s", &grace.as_secs())]);
        self.soft.cancel();
        let hard = self.hard.clone();
        tokio::spawn(async move {
            tokio::select! {
                () = sleep(grace) => hard.cancel(),
                () = hard.cancelled() => {}
            }
        });
    }

    pub fn grace_left(&self) -> Option<Duration> {
        self.grace_deadline
            .get()
            .map(|deadline| deadline.saturating_duration_since(Instant::now()))
    }
}

/// Waits for SIGTERM/SIGINT: the first starts the drain, the second forces it.
pub async fn watch_signals(signals: DrainSignals, grace: Duration, metrics: Arc<Metrics>) {
    let (Ok(mut term), Ok(mut int)) = (signal(SignalKind::terminate()), signal(SignalKind::interrupt())) else {
        logline::warn("signal_handler_failed", &[]);
        return;
    };
    tokio::select! {
        _ = term.recv() => {}
        _ = int.recv() => {}
    }
    signals.begin(grace, &metrics);
    tokio::select! {
        _ = term.recv() => logline::warn("drain_forced", &[]),
        _ = int.recv() => logline::warn("drain_forced", &[]),
        () = signals.hard.cancelled() => return,
    }
    signals.hard.cancel();
}
