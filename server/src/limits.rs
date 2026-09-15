use std::time::Duration;

use monty_pool::ReplConfig;
use monty_proto::pb;
use monty_types::{AssertMessageAnnotations, ResourceLimits, TypeCheckingConfig, TypeCheckingFormat};

/// Server ceilings; `None` means disabled.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct Ceilings {
    pub max_duration_micros: Option<u64>,
    pub max_memory_bytes: Option<u64>,
    pub max_recursion_depth: u64,
}

impl Ceilings {
    pub fn from_args(max_duration_s: u64, max_memory_mib: u64, max_recursion_depth: usize) -> Self {
        Self {
            max_duration_micros: (max_duration_s > 0).then(|| max_duration_s.saturating_mul(1_000_000)),
            max_memory_bytes: (max_memory_mib > 0).then(|| max_memory_mib.saturating_mul(1024 * 1024)),
            max_recursion_depth: max_recursion_depth as u64,
        }
    }

    /// Lowers or fills a client's limits; passes gc_interval and max_suspensions through.
    pub fn clamp(&self, client: Option<pb::ResourceLimits>) -> pb::ResourceLimits {
        let client = client.unwrap_or_default();
        pb::ResourceLimits {
            max_duration_micros: clamp_opt(client.max_duration_micros, self.max_duration_micros),
            max_memory_bytes: clamp_opt(client.max_memory_bytes, self.max_memory_bytes),
            gc_interval: client.gc_interval,
            max_recursion_depth: clamp_opt(client.max_recursion_depth, Some(self.max_recursion_depth)),
            max_suspensions: client.max_suspensions,
        }
    }

    /// The restored session's echoed budget must not exceed the ceilings.
    pub fn admits_restored(&self, event: &pb::ChildEvent) -> bool {
        // NotImplemented: the worker never echoes a restored memory limit, so a dump signed
        // under a higher --max-memory-mib still restores with that limit.
        match self.max_duration_micros {
            Some(ceiling) => event.max_duration_micros.is_some_and(|echoed| echoed <= ceiling),
            None => true,
        }
    }
}

fn clamp_opt(client: Option<u64>, ceiling: Option<u64>) -> Option<u64> {
    match (client, ceiling) {
        (None, ceiling) => ceiling,
        (client, None) => client,
        (Some(client), Some(ceiling)) => Some(client.min(ceiling)),
    }
}

/// Builds the pool's session config from a client Configure and its clamped limits.
pub fn repl_config(configure: &pb::Configure, limits: pb::ResourceLimits) -> ReplConfig {
    let format = pb::TypeCheckFormat::try_from(configure.type_check_format)
        .map(TypeCheckingFormat::from)
        .unwrap_or_default();
    ReplConfig {
        script_name: if configure.script_name.is_empty() {
            ReplConfig::default().script_name
        } else {
            configure.script_name.clone()
        },
        limits: Some(ResourceLimits::from(limits)),
        type_check: configure.type_check,
        type_check_stubs: configure.type_check_stubs.clone(),
        type_check_config: TypeCheckingConfig {
            format,
            color: configure.type_check_color,
        },
        assert_message_annotations: configure.assert_message_annotations.map_or_else(
            AssertMessageAnnotations::default,
            AssertMessageAnnotations::from_max_bytes,
        ),
        print_flush_interval: configure
            .print_flush_interval_ms
            .map(|ms| Duration::from_millis(u64::from(ms))),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ceilings() -> Ceilings {
        Ceilings::from_args(60, 64, 1000)
    }

    #[test]
    fn absent_values_take_the_ceiling() {
        let limits = ceilings().clamp(None);
        assert_eq!(limits.max_duration_micros, Some(60_000_000));
        assert_eq!(limits.max_memory_bytes, Some(64 * 1024 * 1024));
        assert_eq!(limits.max_recursion_depth, Some(1000));
        assert_eq!(limits.max_suspensions, None);
    }

    #[test]
    fn higher_values_are_lowered_and_lower_values_win() {
        let limits = ceilings().clamp(Some(pb::ResourceLimits {
            max_duration_micros: Some(120_000_000),
            max_memory_bytes: Some(1024),
            gc_interval: Some(7),
            max_recursion_depth: Some(5000),
            max_suspensions: Some(3),
        }));
        assert_eq!(limits.max_duration_micros, Some(60_000_000));
        assert_eq!(limits.max_memory_bytes, Some(1024));
        assert_eq!(limits.gc_interval, Some(7));
        assert_eq!(limits.max_recursion_depth, Some(1000));
        assert_eq!(limits.max_suspensions, Some(3));
    }

    #[test]
    fn disabled_ceilings_pass_client_values() {
        let open = Ceilings::from_args(0, 0, 10);
        let limits = open.clamp(Some(pb::ResourceLimits {
            max_memory_bytes: Some(1 << 40),
            ..Default::default()
        }));
        assert_eq!(limits.max_memory_bytes, Some(1 << 40));
        assert_eq!(limits.max_duration_micros, None);
        assert_eq!(limits.max_recursion_depth, Some(10));
    }

    #[test]
    fn restored_budget_is_checked() {
        let mut event = pb::ChildEvent::default();
        assert!(!ceilings().admits_restored(&event));
        event.max_duration_micros = Some(1);
        assert!(ceilings().admits_restored(&event));
        event.max_duration_micros = Some(61_000_000);
        assert!(!ceilings().admits_restored(&event));
        assert!(Ceilings::from_args(0, 0, 10).admits_restored(&pb::ChildEvent::default()));
    }
}
