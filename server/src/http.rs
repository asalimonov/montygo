use std::{net::SocketAddr, sync::Arc};

use axum::{
    Router,
    extract::{
        ConnectInfo, State,
        ws::{WebSocketUpgrade, rejection::WebSocketUpgradeRejection},
    },
    http::{HeaderMap, StatusCode, header},
    response::{IntoResponse, Response},
    routing::get,
};
use monty_proto::MAX_FRAME_LEN;
use tokio_util::task::TaskTracker;

use crate::{
    admission::Rejection, app::Shared, identity::client_id, logline, metrics::RejectReason, session::Session, texts,
};

#[derive(Clone)]
pub struct AppState {
    pub shared: Arc<Shared>,
    pub bound: String,
    pub tracker: TaskTracker,
}

pub fn router(state: AppState) -> Router {
    Router::new()
        .route("/", get(root))
        .route("/health", get(health))
        .route("/metrics", get(metrics))
        .with_state(state)
}

fn plain(status: StatusCode, body: impl Into<String>) -> Response {
    (
        status,
        [(header::CONTENT_TYPE, "text/plain; charset=utf-8")],
        body.into(),
    )
        .into_response()
}

async fn root(
    State(state): State<AppState>,
    ConnectInfo(peer): ConnectInfo<SocketAddr>,
    headers: HeaderMap,
    upgrade: Result<WebSocketUpgrade, WebSocketUpgradeRejection>,
) -> Response {
    let upgrade = match upgrade {
        Ok(upgrade) => upgrade,
        Err(_) if !headers.contains_key(header::UPGRADE) => {
            return plain(StatusCode::OK, texts::info_page(&state.bound));
        }
        Err(rejection) => return rejection.into_response(),
    };
    let shared = Arc::clone(&state.shared);
    let client = client_id(peer, &headers, shared.config.trust_forwarded_for);
    let draining = shared.drain.soft.is_cancelled();
    let permit = match shared.admission.try_admit(client.clone(), draining) {
        Ok(permit) => permit,
        Err(rejection) => {
            let (reason, status, body, label) = match rejection {
                Rejection::Draining => (
                    RejectReason::Draining,
                    StatusCode::SERVICE_UNAVAILABLE,
                    texts::HTTP_DRAINING,
                    "draining",
                ),
                Rejection::Capacity => (
                    RejectReason::Capacity,
                    StatusCode::SERVICE_UNAVAILABLE,
                    texts::HTTP_CAPACITY,
                    "capacity",
                ),
                Rejection::ClientQuota => (
                    RejectReason::ClientQuota,
                    StatusCode::TOO_MANY_REQUESTS,
                    texts::HTTP_CLIENT_QUOTA,
                    "client_quota",
                ),
            };
            shared.metrics.rejection(reason);
            logline::info("rejected", &[("reason", &label), ("client", &client)]);
            return plain(status, body);
        }
    };
    let header_text = |name: &str| headers.get(name).and_then(|v| v.to_str().ok()).map(str::to_owned);
    let trace_parent = header_text("traceparent");
    let user_agent = header_text("user-agent");
    let tracker = state.tracker.clone();
    upgrade
        .max_message_size(MAX_FRAME_LEN as usize)
        .max_frame_size(MAX_FRAME_LEN as usize)
        .on_upgrade(move |socket| tracker.track_future(Session::run(shared, permit, trace_parent, user_agent, socket)))
}

async fn health(State(state): State<AppState>) -> Response {
    if state.shared.drain.soft.is_cancelled() {
        plain(StatusCode::SERVICE_UNAVAILABLE, texts::HTTP_DRAINING)
    } else {
        StatusCode::OK.into_response()
    }
}

async fn metrics(State(state): State<AppState>) -> Response {
    (
        StatusCode::OK,
        [(
            header::CONTENT_TYPE,
            "application/openmetrics-text; version=1.0.0; charset=utf-8",
        )],
        state.shared.metrics.render(),
    )
        .into_response()
}
