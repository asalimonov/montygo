use std::sync::Arc;

use prometheus_client::{
    encoding::{EncodeLabelSet, text::encode},
    metrics::{counter::Counter, family::Family, gauge::Gauge},
    registry::Registry,
};

use crate::version::{MONTY_REV, SERVER_VERSION};

#[derive(Clone, Debug, Hash, PartialEq, Eq, EncodeLabelSet)]
struct OutcomeLabel {
    outcome: &'static str,
}

#[derive(Clone, Copy, Debug, Hash, PartialEq, Eq)]
pub enum Outcome {
    Closed,
    Error,
    Timeout,
    Drained,
    DrainDropped,
}

#[derive(Clone, Debug, Hash, PartialEq, Eq, EncodeLabelSet)]
struct ReasonLabel {
    reason: &'static str,
}

#[derive(Clone, Copy, Debug, Hash, PartialEq, Eq)]
pub enum RejectReason {
    Capacity,
    ClientQuota,
    Draining,
    BadRequest,
}

#[derive(Clone, Debug, Hash, PartialEq, Eq, EncodeLabelSet)]
struct KindLabel {
    kind: &'static str,
}

#[derive(Clone, Copy, Debug, Hash, PartialEq, Eq)]
pub enum TimeoutKind {
    Idle,
    Keepalive,
    Session,
    Turn,
}

#[derive(Clone, Debug, Hash, PartialEq, Eq, EncodeLabelSet)]
struct OpLabel {
    op: &'static str,
}

#[derive(Clone, Copy, Debug, Hash, PartialEq, Eq)]
pub enum DumpOp {
    Signed,
    Verified,
    Rejected,
}

impl Outcome {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Closed => "closed",
            Self::Error => "error",
            Self::Timeout => "timeout",
            Self::Drained => "drained",
            Self::DrainDropped => "drain_dropped",
        }
    }
}

impl RejectReason {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Capacity => "capacity",
            Self::ClientQuota => "client_quota",
            Self::Draining => "draining",
            Self::BadRequest => "bad_request",
        }
    }
}

impl TimeoutKind {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Idle => "idle",
            Self::Keepalive => "keepalive",
            Self::Session => "session",
            Self::Turn => "turn",
        }
    }
}

impl DumpOp {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::Signed => "signed",
            Self::Verified => "verified",
            Self::Rejected => "rejected",
        }
    }
}

#[derive(Clone, Debug, Hash, PartialEq, Eq, EncodeLabelSet)]
struct BuildLabel {
    version: String,
    monty_rev: String,
}

pub struct Metrics {
    registry: Registry,
    pub sessions_active: Gauge,
    sessions_total: Family<OutcomeLabel, Counter>,
    rejections_total: Family<ReasonLabel, Counter>,
    timeouts_total: Family<KindLabel, Counter>,
    dumps_total: Family<OpLabel, Counter>,
    pub workers_spawned_total: Counter,
    pub draining: Gauge,
}

impl std::fmt::Debug for Metrics {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("Metrics")
    }
}

impl Metrics {
    pub fn new() -> Arc<Self> {
        let mut registry = Registry::default();
        let sessions_active = Gauge::default();
        let sessions_total = Family::<OutcomeLabel, Counter>::default();
        let rejections_total = Family::<ReasonLabel, Counter>::default();
        let timeouts_total = Family::<KindLabel, Counter>::default();
        let dumps_total = Family::<OpLabel, Counter>::default();
        let workers_spawned_total = Counter::default();
        let draining = Gauge::default();
        let build_info = Family::<BuildLabel, Gauge>::default();
        build_info
            .get_or_create(&BuildLabel {
                version: SERVER_VERSION.to_owned(),
                monty_rev: MONTY_REV.to_owned(),
            })
            .set(1);
        registry.register(
            "monty_server_sessions_active",
            "Open WebSocket sessions",
            sessions_active.clone(),
        );
        registry.register(
            "monty_server_sessions",
            "Sessions ended, by outcome",
            sessions_total.clone(),
        );
        registry.register(
            "monty_server_rejections",
            "Upgrade requests refused",
            rejections_total.clone(),
        );
        registry.register(
            "monty_server_timeouts",
            "Sessions closed by a deadline",
            timeouts_total.clone(),
        );
        registry.register("monty_server_dumps", "Dump envelope operations", dumps_total.clone());
        registry.register(
            "monty_server_workers_spawned",
            "Worker checkouts started",
            workers_spawned_total.clone(),
        );
        registry.register("monty_server_draining", "1 while the server drains", draining.clone());
        registry.register("monty_server_build_info", "Build information", build_info);
        Arc::new(Self {
            registry,
            sessions_active,
            sessions_total,
            rejections_total,
            timeouts_total,
            dumps_total,
            workers_spawned_total,
            draining,
        })
    }

    pub fn outcome(&self, outcome: Outcome) {
        self.sessions_total
            .get_or_create(&OutcomeLabel {
                outcome: outcome.as_str(),
            })
            .inc();
    }

    pub fn rejection(&self, reason: RejectReason) {
        self.rejections_total
            .get_or_create(&ReasonLabel {
                reason: reason.as_str(),
            })
            .inc();
    }

    pub fn timeout(&self, kind: TimeoutKind) {
        self.timeouts_total
            .get_or_create(&KindLabel { kind: kind.as_str() })
            .inc();
    }

    pub fn dump(&self, op: DumpOp) {
        self.dumps_total.get_or_create(&OpLabel { op: op.as_str() }).inc();
    }

    pub fn render(&self) -> String {
        let mut out = String::new();
        encode(&mut out, &self.registry).expect("writing to a String cannot fail");
        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn renders_families() {
        let metrics = Metrics::new();
        metrics.rejection(RejectReason::ClientQuota);
        metrics.timeout(TimeoutKind::Idle);
        metrics.outcome(Outcome::DrainDropped);
        let text = metrics.render();
        assert!(text.contains("monty_server_sessions_active 0"), "{text}");
        assert!(
            text.contains("monty_server_rejections_total{reason=\"client_quota\"} 1"),
            "{text}"
        );
        assert!(text.contains("monty_server_timeouts_total{kind=\"idle\"} 1"), "{text}");
        assert!(
            text.contains("monty_server_sessions_total{outcome=\"drain_dropped\"} 1"),
            "{text}"
        );
        assert!(text.contains("monty_server_build_info{"), "{text}");
    }
}
