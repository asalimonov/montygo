use std::{
    collections::HashMap,
    sync::{Arc, Mutex, PoisonError, mpsc},
    time::{Duration, Instant},
};

use monty_pool::telemetry::{
    Metrics as PoolMetrics, TelemetryAdapter, TelemetryAdapterHandle, TelemetryContext, configure_telemetry_adapter,
};
use opentelemetry::{
    Context, InstrumentationScope, KeyValue,
    propagation::TextMapPropagator,
    trace::{Span as _, SpanId, SpanKind, TraceId, Tracer as _, TracerProvider as _},
};
use opentelemetry_otlp::{LogExporter, Protocol, SpanExporter, WithExportConfig, WithHttpConfig};
use opentelemetry_sdk::{
    Resource,
    logs::{BatchLogProcessor, LogProcessor, SdkLogRecord},
    propagation::TraceContextPropagator,
    trace::{BatchSpanProcessor, SdkTracer, SdkTracerProvider, SpanData, SpanProcessor},
};

use crate::{config::OtlpConfig, logline, metrics::Outcome};

const METRICS_QUEUE: usize = 16;
const EXPORT_TIMEOUT: Duration = Duration::from_secs(10);
const FAILURE_LOG_INTERVAL: Duration = Duration::from_secs(60);

pub struct Telemetry {
    handle: TelemetryAdapterHandle,
    provider: SdkTracerProvider,
    tracer: SdkTracer,
    adapter: Arc<OtlpAdapter>,
}

struct OtlpAdapter {
    spans: BatchSpanProcessor,
    logs: BatchLogProcessor,
    metrics: Mutex<Option<mpsc::SyncSender<Vec<u8>>>>,
    scope: InstrumentationScope,
}

impl TelemetryAdapter for OtlpAdapter {
    fn start_span(&self, _: &SpanData) -> bool {
        true
    }

    fn end_span(&self, span: &SpanData) -> bool {
        self.spans.on_end(span.clone());
        true
    }

    fn emit_log(&self, _: SpanId, record: &SdkLogRecord) -> bool {
        let mut record = record.clone();
        self.logs.emit(&mut record, &self.scope);
        true
    }

    fn disable_root(&self, _: TraceId, _: SpanId) {}

    fn export_metrics(&self, payload: &[u8]) {
        if let Some(tx) = self.metrics.lock().unwrap_or_else(PoisonError::into_inner).as_ref() {
            let _ = tx.try_send(payload.to_vec());
        }
    }
}

pub struct ConnectionSpan {
    span: opentelemetry_sdk::trace::Span,
}

impl ConnectionSpan {
    pub fn event(&mut self, name: &'static str, detail: &str) {
        self.span
            .add_event(name, vec![KeyValue::new("detail", detail.to_owned())]);
    }

    pub fn end(mut self, outcome: Outcome) {
        self.span
            .set_attribute(KeyValue::new("monty.server.outcome", outcome.as_str()));
        self.span.end();
    }
}

fn headers_from_env() -> HashMap<String, String> {
    std::env::var("OTEL_EXPORTER_OTLP_HEADERS")
        .unwrap_or_default()
        .split(',')
        .filter_map(|pair| pair.split_once('='))
        .map(|(key, value)| (key.trim().to_owned(), value.trim().to_owned()))
        .filter(|(key, _)| !key.is_empty())
        .collect()
}

/// The batch processors export from plain threads, so they need the blocking client.
fn blocking_client() -> Result<reqwest::blocking::Client, String> {
    reqwest::blocking::Client::builder()
        .timeout(EXPORT_TIMEOUT)
        .build()
        .map_err(|err| format!("OTLP HTTP client: {err}"))
}

fn span_exporter(endpoint: &str, headers: &HashMap<String, String>) -> Result<SpanExporter, String> {
    SpanExporter::builder()
        .with_http()
        .with_http_client(blocking_client()?)
        .with_protocol(Protocol::HttpBinary)
        .with_endpoint(format!("{endpoint}/v1/traces"))
        .with_timeout(EXPORT_TIMEOUT)
        .with_headers(headers.clone())
        .build()
        .map_err(|err| format!("OTLP span exporter: {err}"))
}

fn metrics_thread(endpoint: &str, headers: HashMap<String, String>) -> Result<mpsc::SyncSender<Vec<u8>>, String> {
    let (tx, rx) = mpsc::sync_channel::<Vec<u8>>(METRICS_QUEUE);
    let url = format!("{endpoint}/v1/metrics");
    std::thread::Builder::new()
        .name("otlp-metrics".to_owned())
        .spawn(move || {
            let Ok(client) = reqwest::blocking::Client::builder().timeout(EXPORT_TIMEOUT).build() else {
                logline::error("otlp_metrics_client_failed", &[]);
                return;
            };
            let mut last_failure_log: Option<Instant> = None;
            for payload in rx {
                let mut request = client
                    .post(&url)
                    .header("content-type", "application/x-protobuf")
                    .body(payload);
                for (key, value) in &headers {
                    request = request.header(key, value);
                }
                let failure = match request.send() {
                    Ok(response) if response.status().is_success() => None,
                    Ok(response) => Some(format!("status {}", response.status())),
                    Err(err) => Some(err.to_string()),
                };
                if let Some(failure) = failure
                    && last_failure_log.is_none_or(|at| at.elapsed() >= FAILURE_LOG_INTERVAL)
                {
                    last_failure_log = Some(Instant::now());
                    logline::warn("otlp_metrics_export_failed", &[("error", &failure)]);
                }
            }
        })
        .map_err(|err| format!("OTLP metrics thread: {err}"))?;
    Ok(tx)
}

impl Telemetry {
    /// Builds exporters and the pool adapter. MUST run outside a tokio runtime: the blocking
    /// HTTP clients refuse to be created inside one.
    pub fn install(config: &OtlpConfig) -> Result<Arc<Self>, String> {
        // The exporters report failures through tracing; without a subscriber they vanish.
        let _ = tracing_subscriber::fmt()
            .with_max_level(tracing::Level::WARN)
            .with_writer(std::io::stderr)
            .with_ansi(false)
            .try_init();
        let headers = headers_from_env();
        let service = std::env::var("OTEL_SERVICE_NAME").unwrap_or_else(|_| "monty-server".to_owned());
        let resource = Resource::builder().with_service_name(service).build();

        let mut spans = BatchSpanProcessor::builder(span_exporter(&config.endpoint, &headers)?).build();
        spans.set_resource(&resource);
        let log_exporter = LogExporter::builder()
            .with_http()
            .with_http_client(blocking_client()?)
            .with_protocol(Protocol::HttpBinary)
            .with_endpoint(format!("{}/v1/logs", config.endpoint))
            .with_timeout(EXPORT_TIMEOUT)
            .with_headers(headers.clone())
            .build()
            .map_err(|err| format!("OTLP log exporter: {err}"))?;
        let mut logs = BatchLogProcessor::builder(log_exporter).build();
        logs.set_resource(&resource);
        let adapter = Arc::new(OtlpAdapter {
            spans,
            logs,
            metrics: Mutex::new(Some(metrics_thread(&config.endpoint, headers.clone())?)),
            scope: InstrumentationScope::builder("monty-server").build(),
        });
        let handle = configure_telemetry_adapter(Arc::clone(&adapter) as Arc<dyn TelemetryAdapter>)
            .map_err(|err| format!("telemetry pipeline: {err}"))?;
        let provider = SdkTracerProvider::builder()
            .with_resource(resource)
            .with_batch_exporter(span_exporter(&config.endpoint, &headers)?)
            .build();
        let tracer = provider.tracer("monty-server");
        Ok(Arc::new(Self {
            handle,
            provider,
            tracer,
            adapter,
        }))
    }

    pub fn pool_metrics(&self) -> PoolMetrics {
        self.handle.metrics()
    }

    pub fn connection_span(
        &self,
        trace_parent: Option<&str>,
        client: &str,
        user_agent: Option<&str>,
    ) -> ConnectionSpan {
        let parent = trace_parent.map_or_else(Context::new, |value| {
            let carrier = HashMap::from([("traceparent".to_owned(), value.to_owned())]);
            TraceContextPropagator::new().extract(&carrier)
        });
        let mut attributes = vec![KeyValue::new("client.address", client.to_owned())];
        if let Some(user_agent) = user_agent {
            attributes.push(KeyValue::new("user_agent.original", user_agent.to_owned()));
        }
        let span = self
            .tracer
            .span_builder("monty-server connection")
            .with_kind(SpanKind::Server)
            .with_attributes(attributes)
            .start_with_context(&self.tracer, &parent);
        ConnectionSpan { span }
    }

    pub fn checkout_context(&self, span: &ConnectionSpan) -> Option<TelemetryContext> {
        let context = span.span.span_context();
        self.handle
            .context_from_ids(
                context.trace_id(),
                context.span_id(),
                context.trace_flags().to_u8(),
                "",
                false,
            )
            .ok()
    }

    /// Flushes and stops every exporter. Blocking: call from a blocking thread.
    pub fn shutdown(&self) {
        let _ = self.handle.force_flush();
        let _ = self.provider.shutdown();
        let _ = self.adapter.spans.shutdown();
        let _ = self.adapter.logs.shutdown();
        self.adapter
            .metrics
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .take();
    }
}
