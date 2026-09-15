use std::time::Duration;

use monty_proto::PROTOCOL_VERSION;
use serde::Serialize;

use crate::{
    config::Config,
    version::{MONTY_REV, SERVER_VERSION},
};

/// Body of `GET /info`.
#[derive(Debug, Clone, Serialize)]
pub struct ServerInfo {
    pub version: &'static str,
    pub monty_rev: &'static str,
    pub protocol_version: u32,
    pub limits: Limits,
}

/// Effective limits; a disabled timeout or ceiling is 0.
#[derive(Debug, Clone, Serialize)]
pub struct Limits {
    pub idle_timeout_s: u64,
    pub keepalive_s: u64,
    pub session_timeout_s: u64,
    pub turn_timeout_s: u64,
    pub max_duration_s: u64,
    pub max_memory_bytes: u64,
    pub max_recursion_depth: u64,
    pub max_sessions: usize,
    pub max_sessions_per_client: usize,
}

impl ServerInfo {
    pub fn from_config(config: &Config) -> Self {
        Self {
            version: SERVER_VERSION,
            monty_rev: MONTY_REV,
            protocol_version: PROTOCOL_VERSION,
            limits: Limits {
                idle_timeout_s: secs(config.idle_timeout),
                keepalive_s: secs(config.keepalive),
                session_timeout_s: secs(config.session_timeout),
                turn_timeout_s: secs(config.turn_timeout),
                max_duration_s: config.ceilings.max_duration_micros.unwrap_or(0) / 1_000_000,
                max_memory_bytes: config.ceilings.max_memory_bytes.unwrap_or(0),
                max_recursion_depth: config.ceilings.max_recursion_depth,
                max_sessions: config.max_sessions,
                max_sessions_per_client: config.max_sessions_per_client.unwrap_or(0),
            },
        }
    }
}

fn secs(value: Option<Duration>) -> u64 {
    value.map_or(0, |d| d.as_secs())
}

#[cfg(test)]
mod tests {
    use std::{path::PathBuf, sync::Arc};

    use crate::{envelope::DumpKeys, limits::Ceilings};

    use super::*;

    #[test]
    fn disabled_limits_are_zero() {
        let config = Config {
            bind: "127.0.0.1:0".to_owned(),
            monty_bin: PathBuf::from("/bin/sh"),
            max_sessions: 3,
            max_sessions_per_client: None,
            idle_timeout: None,
            keepalive: Some(Duration::from_secs(5)),
            session_timeout: Some(Duration::from_secs(3600)),
            turn_timeout: None,
            drain_grace: Duration::from_secs(30),
            ceilings: Ceilings::from_args(0, 0, 1000),
            trust_forwarded_for: false,
            dump_keys: Arc::new(DumpKeys::new(b"0123456789abcdef", None).unwrap()),
            otlp: None,
        };
        let info = ServerInfo::from_config(&config);
        assert_eq!(info.version, SERVER_VERSION);
        assert_eq!(info.monty_rev, MONTY_REV);
        assert_eq!(info.protocol_version, PROTOCOL_VERSION);
        let json = serde_json::to_value(&info).unwrap();
        assert_eq!(
            json["limits"],
            serde_json::json!({
                "idle_timeout_s": 0,
                "keepalive_s": 5,
                "session_timeout_s": 3600,
                "turn_timeout_s": 0,
                "max_duration_s": 0,
                "max_memory_bytes": 0,
                "max_recursion_depth": 1000,
                "max_sessions": 3,
                "max_sessions_per_client": 0,
            })
        );
    }

    #[test]
    fn enabled_limits_are_reported() {
        let ceilings = Ceilings::from_args(60, 64, 500);
        let config = Config {
            bind: "127.0.0.1:0".to_owned(),
            monty_bin: PathBuf::from("/bin/sh"),
            max_sessions: 64,
            max_sessions_per_client: Some(10),
            idle_timeout: Some(Duration::from_secs(60)),
            keepalive: None,
            session_timeout: None,
            turn_timeout: Some(Duration::from_secs(300)),
            drain_grace: Duration::from_secs(30),
            ceilings,
            trust_forwarded_for: false,
            dump_keys: Arc::new(DumpKeys::new(b"0123456789abcdef", None).unwrap()),
            otlp: None,
        };
        let limits = ServerInfo::from_config(&config).limits;
        assert_eq!(limits.max_duration_s, 60);
        assert_eq!(limits.max_memory_bytes, 64 * 1024 * 1024);
        assert_eq!(limits.max_recursion_depth, 500);
        assert_eq!(limits.max_sessions, 64);
        assert_eq!(limits.max_sessions_per_client, 10);
        assert_eq!(limits.idle_timeout_s, 60);
        assert_eq!(limits.keepalive_s, 0);
        assert_eq!(limits.turn_timeout_s, 300);
    }
}
