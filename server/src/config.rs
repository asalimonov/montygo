use std::{
    env,
    os::unix::fs::PermissionsExt,
    path::{Path, PathBuf},
    sync::Arc,
    time::Duration,
};

use crate::{envelope::DumpKeys, limits::Ceilings};

#[derive(clap::Parser, Debug, Clone)]
#[command(
    name = "monty-server",
    version,
    about = "WebSocket server hosting Monty sandbox workers"
)]
pub struct Cli {
    #[command(subcommand)]
    pub command: Option<Command>,
    #[command(flatten)]
    pub serve: ServeArgs,
}

#[derive(clap::Subcommand, Debug, Clone)]
pub enum Command {
    /// Exit 0 when the health endpoint answers 200.
    Probe {
        #[arg(long)]
        url: Option<String>,
    },
}

#[derive(clap::Args, Debug, Clone)]
pub struct ServeArgs {
    /// Interface to bind.
    #[arg(long, env = "MONTY_SERVER_HOST", default_value = "127.0.0.1")]
    pub host: String,
    /// Port to bind; 0 selects an ephemeral port.
    #[arg(long, env = "MONTY_SERVER_PORT", default_value_t = 8000)]
    pub port: u16,
    /// Worker binary.
    #[arg(long, env = "MONTY_BIN", default_value = "monty")]
    pub monty_bin: PathBuf,
    /// Concurrent sessions across the server.
    #[arg(long, env = "MONTY_SERVER_MAX_SESSIONS", default_value_t = 64)]
    pub max_sessions: usize,
    /// Concurrent sessions per caller; 0 disables.
    #[arg(long, env = "MONTY_SERVER_MAX_SESSIONS_PER_CLIENT", default_value_t = 10)]
    pub max_sessions_per_client: usize,
    /// Maximum gap between requests in seconds; 0 disables.
    #[arg(long, env = "MONTY_SERVER_IDLE_TIMEOUT", default_value_t = 60)]
    pub idle_timeout: u64,
    /// WebSocket ping interval in seconds; 0 disables.
    #[arg(long, env = "MONTY_SERVER_KEEPALIVE", default_value_t = 5)]
    pub keepalive: u64,
    /// Maximum total session lifetime in seconds; 0 disables.
    #[arg(long, env = "MONTY_SERVER_SESSION_TIMEOUT", default_value_t = 3600)]
    pub session_timeout: u64,
    /// Wall-clock cap on one request in seconds, host callbacks included; 0 disables.
    #[arg(long, env = "MONTY_SERVER_TURN_TIMEOUT", default_value_t = 300)]
    pub turn_timeout: u64,
    /// Seconds after SIGTERM for existing sessions to collect a dump.
    #[arg(long, env = "MONTY_SERVER_DRAIN_GRACE", default_value_t = 30)]
    pub drain_grace: u64,
    /// Per-session memory ceiling in MiB; 0 disables.
    #[arg(long, env = "MONTY_SERVER_MAX_MEMORY_MIB", default_value_t = 64)]
    pub max_memory_mib: u64,
    /// Cumulative sandbox execution time per session in seconds; 0 disables.
    #[arg(long, env = "MONTY_SERVER_MAX_DURATION", default_value_t = 60)]
    pub max_duration: u64,
    /// Per-session call-stack ceiling; cannot be disabled.
    #[arg(long, env = "MONTY_SERVER_MAX_RECURSION_DEPTH", default_value_t = 1000)]
    pub max_recursion_depth: usize,
    /// Use the last X-Forwarded-For entry as the caller identity.
    #[arg(long, env = "MONTY_SERVER_TRUST_FORWARDED_FOR")]
    pub trust_forwarded_for: bool,
    /// Key of at least 16 bytes for signing session dumps.
    #[arg(long, env = "MONTY_SERVER_DUMP_KEY", hide_env_values = true)]
    pub dump_key: Option<String>,
    /// Previous dump key, accepted for verification only.
    #[arg(long, env = "MONTY_SERVER_DUMP_KEY_PREVIOUS", hide_env_values = true)]
    pub dump_key_previous: Option<String>,
    /// OTLP collector base URL; unset disables export.
    #[arg(long, env = "OTEL_EXPORTER_OTLP_ENDPOINT")]
    pub otlp_endpoint: Option<String>,
    /// OTLP transport.
    #[arg(long, env = "OTEL_EXPORTER_OTLP_PROTOCOL", default_value = "http/protobuf")]
    pub otlp_protocol: OtlpProtocol,
}

#[derive(clap::ValueEnum, Debug, Clone, Copy, PartialEq, Eq)]
pub enum OtlpProtocol {
    #[value(name = "http/protobuf")]
    HttpProtobuf,
    Grpc,
}

#[derive(Debug, Clone)]
pub struct Config {
    pub bind: String,
    pub monty_bin: PathBuf,
    pub max_sessions: usize,
    pub max_sessions_per_client: Option<usize>,
    pub idle_timeout: Option<Duration>,
    pub keepalive: Option<Duration>,
    pub session_timeout: Option<Duration>,
    pub turn_timeout: Option<Duration>,
    pub drain_grace: Duration,
    pub ceilings: Ceilings,
    pub trust_forwarded_for: bool,
    pub dump_keys: Arc<DumpKeys>,
    pub otlp: Option<OtlpConfig>,
}

#[derive(Debug, Clone)]
pub struct OtlpConfig {
    pub endpoint: String,
}

#[derive(Debug, PartialEq, Eq)]
pub struct ConfigError(pub String);

impl std::fmt::Display for ConfigError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for ConfigError {}

fn err(message: impl Into<String>) -> ConfigError {
    ConfigError(message.into())
}

impl ServeArgs {
    pub fn validate(self) -> Result<Config, ConfigError> {
        let dump_key = self
            .dump_key
            .ok_or_else(|| err("--dump-key is required (MONTY_SERVER_DUMP_KEY), at least 16 bytes"))?;
        let dump_keys = DumpKeys::new(
            dump_key.as_bytes(),
            self.dump_key_previous.as_deref().map(str::as_bytes),
        )
        .map_err(ConfigError)?;
        if self.max_sessions == 0 {
            return Err(err("--max-sessions must be at least 1"));
        }
        if self.max_recursion_depth == 0 {
            return Err(err("--max-recursion-depth cannot be disabled"));
        }
        let otlp = match self.otlp_endpoint.filter(|endpoint| !endpoint.is_empty()) {
            None => None,
            Some(endpoint) => {
                if !(endpoint.starts_with("http://") || endpoint.starts_with("https://")) {
                    return Err(err("--otlp-endpoint must be an http(s) URL"));
                }
                if self.otlp_protocol == OtlpProtocol::Grpc {
                    return Err(err(
                        "OTLP gRPC export is not supported; use --otlp-protocol http/protobuf",
                    ));
                }
                Some(OtlpConfig {
                    endpoint: endpoint.trim_end_matches('/').to_owned(),
                })
            }
        };
        Ok(Config {
            bind: if self.host.contains(':') {
                format!("[{}]:{}", self.host, self.port)
            } else {
                format!("{}:{}", self.host, self.port)
            },
            monty_bin: resolve_binary(&self.monty_bin)?,
            max_sessions: self.max_sessions,
            max_sessions_per_client: (self.max_sessions_per_client > 0).then_some(self.max_sessions_per_client),
            idle_timeout: secs(self.idle_timeout),
            keepalive: secs(self.keepalive),
            session_timeout: secs(self.session_timeout),
            turn_timeout: secs(self.turn_timeout),
            drain_grace: Duration::from_secs(self.drain_grace),
            ceilings: Ceilings::from_args(self.max_duration, self.max_memory_mib, self.max_recursion_depth),
            trust_forwarded_for: self.trust_forwarded_for,
            dump_keys: Arc::new(dump_keys),
            otlp,
        })
    }
}

fn secs(value: u64) -> Option<Duration> {
    (value > 0).then(|| Duration::from_secs(value))
}

fn is_executable(path: &Path) -> bool {
    path.metadata()
        .is_ok_and(|meta| meta.is_file() && meta.permissions().mode() & 0o111 != 0)
}

fn resolve_binary(path: &Path) -> Result<PathBuf, ConfigError> {
    if path.components().count() > 1 || path.is_absolute() {
        return if is_executable(path) {
            Ok(path.to_path_buf())
        } else {
            Err(err(format!(
                "worker binary {} is not an executable file",
                path.display()
            )))
        };
    }
    env::var_os("PATH")
        .into_iter()
        .flat_map(|paths| env::split_paths(&paths).collect::<Vec<_>>())
        .map(|dir| dir.join(path))
        .find(|candidate| is_executable(candidate))
        .ok_or_else(|| err(format!("worker binary {} was not found on PATH", path.display())))
}

#[cfg(test)]
mod tests {
    use clap::Parser;

    use super::*;

    fn args(extra: &[&str]) -> Result<Config, ConfigError> {
        let mut argv = vec!["monty-server", "--monty-bin", "/bin/sh"];
        argv.extend_from_slice(extra);
        Cli::try_parse_from(argv).expect("parse").serve.validate()
    }

    #[test]
    fn dump_key_is_required_and_long_enough() {
        assert!(args(&[]).unwrap_err().0.contains("--dump-key is required"));
        assert!(
            args(&["--dump-key", "short"])
                .unwrap_err()
                .0
                .contains("at least 16 bytes")
        );
        assert!(args(&["--dump-key", "0123456789abcdef"]).is_ok());
    }

    #[test]
    fn zero_rules() {
        let key = ["--dump-key", "0123456789abcdef"];
        let config = args(&[&key[..], &["--idle-timeout", "0", "--max-sessions-per-client", "0"]].concat()).unwrap();
        assert_eq!(config.idle_timeout, None);
        assert_eq!(config.max_sessions_per_client, None);
        assert_eq!(config.keepalive, Some(Duration::from_secs(5)));
        assert!(args(&[&key[..], &["--max-recursion-depth", "0"]].concat()).is_err());
        assert!(args(&[&key[..], &["--max-sessions", "0"]].concat()).is_err());
    }

    #[test]
    fn otlp_rules() {
        let key = ["--dump-key", "0123456789abcdef"];
        assert!(args(&[&key[..], &["--otlp-endpoint", "collector:4318"]].concat()).is_err());
        assert!(
            args(
                &[
                    &key[..],
                    &["--otlp-endpoint", "http://c:4318", "--otlp-protocol", "grpc"]
                ]
                .concat()
            )
            .is_err()
        );
        let config = args(&[&key[..], &["--otlp-endpoint", "http://c:4318/"]].concat()).unwrap();
        assert_eq!(config.otlp.unwrap().endpoint, "http://c:4318");
    }

    #[test]
    fn binary_resolution() {
        assert!(resolve_binary(Path::new("/nonexistent/monty")).is_err());
        assert!(resolve_binary(Path::new("sh")).is_ok());
    }
}
