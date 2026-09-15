use std::{io, net::SocketAddr, sync::Arc, time::Duration};

use monty_pool::{Pool, PoolConfig, PoolError};
use tokio::{net::TcpListener, task::JoinHandle, time::sleep};
use tokio_util::task::TaskTracker;

use crate::{
    admission::Admission,
    config::Config,
    drain::{DrainSignals, watch_signals},
    http::{AppState, router},
    logline,
    metrics::Metrics,
    telemetry::Telemetry,
    version::MONTY_REV,
};

const CHECKOUT_TIMEOUT: Duration = Duration::from_secs(5);
const HARD_STOP_BUDGET: Duration = Duration::from_secs(3);

pub struct Shared {
    pub pool: Pool,
    pub config: Config,
    pub metrics: Arc<Metrics>,
    pub drain: DrainSignals,
    pub telemetry: Option<Arc<Telemetry>>,
    pub admission: Arc<Admission>,
}

#[derive(Debug, Clone, Copy)]
pub struct StartOptions {
    pub print_url: bool,
    pub watch_signals: bool,
}

pub struct Server {
    pub bound: SocketAddr,
    pub drain: DrainSignals,
    pub metrics: Arc<Metrics>,
    grace: Duration,
    handle: JoinHandle<Result<(), ServeError>>,
}

#[derive(Debug)]
pub enum ServeError {
    Bind(io::Error),
    Pool(PoolError),
    Io(io::Error),
}

impl std::fmt::Display for ServeError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Bind(err) => write!(f, "bind: {err}"),
            Self::Pool(err) => write!(f, "worker pool: {err}"),
            Self::Io(err) => write!(f, "serve: {err}"),
        }
    }
}

impl std::error::Error for ServeError {}

/// Binds, builds the pool and router, optionally prints the bound URL, and serves until drained.
pub async fn start(
    config: Config,
    telemetry: Option<Arc<Telemetry>>,
    options: StartOptions,
) -> Result<Server, ServeError> {
    let metrics = Metrics::new();
    let mut pool_config = PoolConfig::subprocess(&config.monty_bin);
    pool_config.min_processes = 0;
    pool_config.max_processes = config.max_sessions;
    pool_config.checkout_timeout = Some(CHECKOUT_TIMEOUT);
    pool_config.request_timeout = config.turn_timeout;
    pool_config.max_checkouts_per_worker = Some(1);
    pool_config.metrics = telemetry.as_ref().map(|t| t.pool_metrics());
    let pool = Pool::new(pool_config).await.map_err(ServeError::Pool)?;

    let listener = TcpListener::bind(&config.bind).await.map_err(ServeError::Bind)?;
    let bound = listener.local_addr().map_err(ServeError::Bind)?;
    if options.print_url {
        println!("ws://{bound}/");
    }
    logline::info("listening", &[("addr", &bound), ("monty_rev", &MONTY_REV)]);

    let drain = DrainSignals::new();
    let grace = config.drain_grace;
    if options.watch_signals {
        tokio::spawn(watch_signals(drain.clone(), grace, Arc::clone(&metrics)));
    }
    let admission = Admission::new(
        config.max_sessions,
        config.max_sessions_per_client,
        Arc::clone(&metrics),
    );
    let shared = Arc::new(Shared {
        pool,
        config,
        metrics: Arc::clone(&metrics),
        drain: drain.clone(),
        telemetry,
        admission,
    });
    let tracker = TaskTracker::new();
    let app = router(AppState {
        shared: Arc::clone(&shared),
        bound: bound.to_string(),
        tracker: tracker.clone(),
    });
    let soft = drain.soft.clone();
    let hard = drain.hard.clone();
    let handle = tokio::spawn(async move {
        let served = axum::serve(listener, app.into_make_service_with_connect_info::<SocketAddr>())
            .with_graceful_shutdown(async move { soft.cancelled().await })
            .await
            .map_err(ServeError::Io);
        tracker.close();
        tokio::select! {
            () = tracker.wait() => {}
            () = async { hard.cancelled().await; sleep(HARD_STOP_BUDGET).await } => {}
        }
        shared.pool.close().await;
        if let Some(telemetry) = shared.telemetry.clone() {
            let _ = tokio::task::spawn_blocking(move || telemetry.shutdown()).await;
        }
        logline::info("drain_finished", &[]);
        served
    });
    Ok(Server {
        bound,
        drain,
        metrics,
        grace,
        handle,
    })
}

impl Server {
    /// Starts the drain as SIGTERM would; a second call forces it.
    pub fn begin_drain(&self) {
        self.drain.begin(self.grace, &self.metrics);
    }

    pub async fn wait(self) -> Result<(), ServeError> {
        match self.handle.await {
            Ok(result) => result,
            Err(err) => Err(ServeError::Io(io::Error::other(err))),
        }
    }
}
