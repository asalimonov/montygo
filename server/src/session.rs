use std::{
    sync::{
        Arc,
        atomic::{AtomicU64, Ordering},
    },
    time::Duration,
};

use axum::extract::ws::WebSocket;
use bytes::Bytes;
use futures_util::StreamExt;
use monty_pool::{Checkout, CheckoutOptions, PoolError, PrintFuture};
use monty_proto::{MAX_FRAME_LEN, check_protocol_version, pb};
use monty_types::ExcType;
use pb::{child_event::Kind as Event, parent_request::Kind as Request};
use tokio::{
    sync::mpsc,
    time::{Instant, sleep_until, timeout},
};

use crate::{
    admission::SessionPermit,
    app::Shared,
    envelope::HEADER_LEN,
    events,
    inbound::{Inbound, reader},
    logline,
    metrics::{DumpOp, Outcome, TimeoutKind},
    outbound::{CLOSE_GOING_AWAY, CLOSE_INTERNAL, CLOSE_NORMAL, CLOSE_POLICY, OutboundTx, writer},
    telemetry::ConnectionSpan,
    texts,
};

const OUTBOUND_DEPTH: usize = 16;
const FINISH_BUDGET: Duration = Duration::from_secs(5);
const WRITER_BUDGET: Duration = Duration::from_secs(2);

/// Why a session ended; drives the outcome metric and the log line.
#[derive(Debug)]
pub enum SessionEnd {
    ClientClosed,
    Violation(String),
    Timeout(TimeoutKind),
    WorkerFailed,
    Drained,
    DrainDropped,
    WriteFailed,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum RequestKind {
    Resume,
    Load,
    Dump,
    Other,
}

enum Interrupt {
    Timeout(TimeoutKind),
    Text,
    Hard,
    End(SessionEnd),
}

pub struct Session {
    shared: Arc<Shared>,
    client: String,
    started: Instant,
    out: OutboundTx,
    last_pong_ms: Arc<AtomicU64>,
    ping_sent_ms: Option<u64>,
    keepalive_at: Option<Instant>,
    idle_deadline: Option<Instant>,
    turn_deadline: Option<Instant>,
    session_deadline: Option<Instant>,
    draining: bool,
    suspended: bool,
    configured: bool,
    pending: Option<Bytes>,
    span: Option<ConnectionSpan>,
}

async fn sleep_opt(deadline: Option<Instant>) {
    match deadline {
        Some(deadline) => sleep_until(deadline).await,
        None => std::future::pending().await,
    }
}

fn no_events(_: &pb::ChildEvent) -> PrintFuture {
    Box::pin(std::future::ready(()))
}

fn request_kind(request: &pb::ParentRequest) -> RequestKind {
    match request.kind {
        Some(
            Request::ResumeCall(_) | Request::ResumeNameLookup(_) | Request::ResumeFutures(_) | Request::AbortFeed(_),
        ) => RequestKind::Resume,
        Some(Request::Load(_)) => RequestKind::Load,
        Some(Request::Dump(_)) => RequestKind::Dump,
        _ => RequestKind::Other,
    }
}

impl Session {
    pub async fn run(
        shared: Arc<Shared>,
        permit: SessionPermit,
        trace_parent: Option<String>,
        user_agent: Option<String>,
        socket: WebSocket,
    ) {
        let (sink, stream) = socket.split();
        let (out_tx, out_rx) = mpsc::channel(OUTBOUND_DEPTH);
        let writer_task = tokio::spawn(writer(sink, out_rx));
        let (in_tx, mut in_rx) = mpsc::channel(1);
        let started = Instant::now();
        let last_pong_ms = Arc::new(AtomicU64::new(0));
        let reader_task = tokio::spawn(reader(stream, in_tx, Arc::clone(&last_pong_ms), started));
        let client = permit.client().to_owned();
        let span = shared
            .telemetry
            .as_ref()
            .map(|telemetry| telemetry.connection_span(trace_parent.as_deref(), &client, user_agent.as_deref()));
        let config = &shared.config;
        let mut session = Self {
            client,
            started,
            out: OutboundTx::new(out_tx),
            last_pong_ms,
            ping_sent_ms: None,
            keepalive_at: config.keepalive.map(|interval| started + interval),
            idle_deadline: config.idle_timeout.map(|limit| started + limit),
            turn_deadline: None,
            session_deadline: config.session_timeout.map(|limit| started + limit),
            draining: false,
            suspended: false,
            configured: false,
            pending: None,
            span,
            shared: Arc::clone(&shared),
        };
        logline::info("session_start", &[("client", &session.client)]);

        let mut checkout = None;
        let end = session.serve(&mut in_rx, &mut checkout).await;
        if let Some(checkout) = checkout.take()
            && matches!(end, SessionEnd::ClientClosed)
            && !session.suspended
        {
            let _ = timeout(FINISH_BUDGET, checkout.finish()).await;
        }
        session.record_end(&end);
        drop(session);
        let _ = timeout(WRITER_BUDGET, writer_task).await;
        reader_task.abort();
        drop(permit);
    }

    async fn serve(&mut self, inbound: &mut mpsc::Receiver<Inbound>, checkout: &mut Option<Checkout>) -> SessionEnd {
        loop {
            let bytes = match self.next_request(inbound).await {
                Ok(bytes) => bytes,
                Err(end) => return end,
            };
            self.idle_deadline = None;
            if let Err(end) = self.on_request(bytes, inbound, checkout).await {
                return end;
            }
        }
    }

    async fn next_request(&mut self, inbound: &mut mpsc::Receiver<Inbound>) -> Result<Bytes, SessionEnd> {
        if let Some(bytes) = self.pending.take() {
            return Ok(bytes);
        }
        let soft = self.shared.drain.soft.clone();
        let hard = self.shared.drain.hard.clone();
        loop {
            let (idle, session, keepalive) = (self.idle_deadline, self.session_deadline, self.keepalive_at);
            let turn = self.turn_deadline;
            tokio::select! {
                message = inbound.recv() => return match message {
                    Some(Inbound::Request(bytes)) => Ok(bytes),
                    Some(Inbound::Text) => Err(self.violation(texts::CLOSE_TEXT_MESSAGE).await),
                    Some(Inbound::Closed) | None => Err(SessionEnd::ClientClosed),
                },
                () = sleep_opt(idle) => return Err(self.timed_out(TimeoutKind::Idle).await),
                () = sleep_opt(turn) => return Err(self.timed_out(TimeoutKind::Turn).await),
                () = sleep_opt(session) => return Err(self.timed_out(TimeoutKind::Session).await),
                () = sleep_opt(keepalive) => self.keepalive_tick().await?,
                () = soft.cancelled(), if !self.draining => self.draining = true,
                () = hard.cancelled() => return Err(self.drain_dropped().await),
            }
        }
    }

    async fn on_request(
        &mut self,
        bytes: Bytes,
        inbound: &mut mpsc::Receiver<Inbound>,
        checkout: &mut Option<Checkout>,
    ) -> Result<(), SessionEnd> {
        let mut request = match events::decode_request(&bytes) {
            Ok(request) => request,
            Err(err) => return Err(self.violation(&texts::close_malformed(&err)).await),
        };
        if request.kind.is_none() {
            return Err(self.violation(texts::CLOSE_EMPTY_REQUEST).await);
        }
        if self.draining {
            return Err(self.shutdown_dump(checkout).await);
        }
        match request.kind.take() {
            Some(Request::Configure(_)) if self.configured => {
                return Err(self.violation(texts::CLOSE_ALREADY_CONFIGURED).await);
            }
            Some(Request::Configure(configure)) => return self.configure(configure, checkout).await,
            Some(Request::Reset(_) | Request::Shutdown(_)) => {
                return Err(self.violation(texts::CLOSE_LIFECYCLE).await);
            }
            kind => request.kind = kind,
        }
        if checkout.is_none() {
            return Err(self.violation(texts::CLOSE_EXPECTED_CONFIGURE).await);
        }
        if let Some(Request::Load(load)) = &mut request.kind {
            match self.shared.config.dump_keys.verify(&load.state) {
                Ok(raw) => {
                    self.shared.metrics.dump(DumpOp::Verified);
                    load.state = raw.to_vec();
                }
                Err(err) => {
                    self.shared.metrics.dump(DumpOp::Rejected);
                    logline::info("dump_rejected", &[("client", &self.client), ("cause", &err)]);
                    self.send(&events::error(ExcType::ValueError, texts::INVALID_DUMP))
                        .await?;
                    self.arm_idle();
                    return Ok(());
                }
            }
        }
        self.turn(request, inbound, checkout).await
    }

    async fn configure(&mut self, configure: pb::Configure, checkout: &mut Option<Checkout>) -> Result<(), SessionEnd> {
        if let Err(refusal) = check_protocol_version(configure.protocol_version) {
            let _ = self.send(&events::fatal(&refusal)).await;
            self.out.close(CLOSE_NORMAL, "").await;
            return Err(SessionEnd::WorkerFailed);
        }
        let shared = Arc::clone(&self.shared);
        let limits = shared.config.ceilings.clamp(configure.limits);
        let repl = crate::limits::repl_config(&configure, limits);
        let mut options = CheckoutOptions::default();
        if let (Some(telemetry), Some(span)) = (&shared.telemetry, &self.span) {
            options = options.with_telemetry(telemetry.checkout_context(span));
        }
        let deadline = shared.config.turn_timeout.map(|limit| Instant::now() + limit);
        let hard = shared.drain.hard.clone();
        let result = tokio::select! {
            result = shared.pool.checkout_with(&repl, options) => result,
            () = sleep_opt(deadline) => return Err(self.timed_out(TimeoutKind::Turn).await),
            () = hard.cancelled() => return Err(self.drain_dropped().await),
        };
        match result {
            Ok(fresh) => {
                shared.metrics.workers_spawned_total.inc();
                *checkout = Some(fresh);
                self.configured = true;
                self.send(&events::ok(&limits)).await?;
                self.arm_idle();
                Ok(())
            }
            Err(err) => Err(self.fail(err, checkout).await),
        }
    }

    async fn turn(
        &mut self,
        request: pb::ParentRequest,
        inbound: &mut mpsc::Receiver<Inbound>,
        checkout: &mut Option<Checkout>,
    ) -> Result<(), SessionEnd> {
        let kind = request_kind(&request);
        if !self.suspended && kind != RequestKind::Resume {
            self.turn_deadline = self.shared.config.turn_timeout.map(|limit| Instant::now() + limit);
        }
        let out = self.out.clone();
        let mut on_event = move |event: &pb::ChildEvent| -> PrintFuture {
            let out = out.clone();
            let bytes = events::encode(event);
            Box::pin(async move {
                if let Ok(bytes) = bytes {
                    let _ = out.bytes(bytes).await;
                }
            })
        };
        let soft = self.shared.drain.soft.clone();
        let hard = self.shared.drain.hard.clone();
        let active = checkout.as_mut().expect("a turn requires a checkout");
        let outcome = {
            let turn = active.turn_raw(&request, &mut on_event);
            tokio::pin!(turn);
            loop {
                let (turn_at, session_at, keepalive_at) =
                    (self.turn_deadline, self.session_deadline, self.keepalive_at);
                let listen = self.pending.is_none();
                tokio::select! {
                    result = &mut turn => break Ok(result),
                    () = sleep_opt(turn_at) => break Err(Interrupt::Timeout(TimeoutKind::Turn)),
                    () = sleep_opt(session_at) => break Err(Interrupt::Timeout(TimeoutKind::Session)),
                    () = sleep_opt(keepalive_at) => {
                        if let Err(end) = self.keepalive_tick().await {
                            break Err(Interrupt::End(end));
                        }
                    }
                    message = inbound.recv(), if listen => match message {
                        Some(Inbound::Request(bytes)) => self.pending = Some(bytes),
                        Some(Inbound::Text) => break Err(Interrupt::Text),
                        Some(Inbound::Closed) | None => break Err(Interrupt::End(SessionEnd::ClientClosed)),
                    },
                    () = soft.cancelled(), if !self.draining => self.draining = true,
                    () = hard.cancelled() => break Err(Interrupt::Hard),
                }
            }
        };
        match outcome {
            Ok(Ok(event)) => self.forward(kind, event, checkout).await,
            Ok(Err(err)) => Err(self.fail(err, checkout).await),
            Err(interrupt) => {
                checkout.take();
                Err(match interrupt {
                    Interrupt::Timeout(kind) => self.timed_out(kind).await,
                    Interrupt::Text => self.violation(texts::CLOSE_TEXT_MESSAGE).await,
                    Interrupt::Hard => self.drain_dropped().await,
                    Interrupt::End(end) => end,
                })
            }
        }
    }

    async fn forward(
        &mut self,
        kind: RequestKind,
        mut event: pb::ChildEvent,
        checkout: &mut Option<Checkout>,
    ) -> Result<(), SessionEnd> {
        match &mut event.kind {
            Some(Event::DumpResult(result)) => match self.shared.config.dump_keys.sign(&result.state) {
                Some(envelope) => {
                    self.shared.metrics.dump(DumpOp::Signed);
                    result.state = envelope;
                }
                None => {
                    let message = texts::frame_too_large(result.state.len() + HEADER_LEN, MAX_FRAME_LEN);
                    checkout.take();
                    let _ = self.send(&events::fatal(&message)).await;
                    self.out.close(CLOSE_INTERNAL, "").await;
                    return Err(SessionEnd::WorkerFailed);
                }
            },
            Some(Event::FatalError(_)) => {
                checkout.take();
                let _ = self.send(&event).await;
                self.out.close(CLOSE_NORMAL, "").await;
                return Err(SessionEnd::WorkerFailed);
            }
            _ => {}
        }
        let suspension = events::is_suspension(&event);
        if kind == RequestKind::Load
            && (suspension || events::is_ok(&event))
            && !self.shared.config.ceilings.admits_restored(&event)
        {
            checkout.take();
            return Err(self.violation(texts::CLOSE_RESTORED_OVER_LIMITS).await);
        }
        self.send(&event).await?;
        if kind == RequestKind::Dump {
            if !self.suspended {
                self.turn_deadline = None;
            }
        } else if suspension {
            if kind == RequestKind::Load {
                self.turn_deadline = self.shared.config.turn_timeout.map(|limit| Instant::now() + limit);
            }
            self.suspended = true;
        } else {
            self.suspended = false;
            self.turn_deadline = None;
        }
        self.arm_idle();
        Ok(())
    }

    async fn fail(&mut self, err: PoolError, checkout: &mut Option<Checkout>) -> SessionEnd {
        checkout.take();
        logline::warn("worker_failed", &[("client", &self.client), ("error", &err)]);
        let event = match &err {
            PoolError::Runtime(exception) => events::error_from(exception),
            other => events::fatal(&other.to_string()),
        };
        let _ = self.send(&event).await;
        self.out.close(CLOSE_INTERNAL, "").await;
        SessionEnd::WorkerFailed
    }

    async fn shutdown_dump(&mut self, checkout: &mut Option<Checkout>) -> SessionEnd {
        let mut dump = None;
        if let Some(active) = checkout.as_mut() {
            let unbounded = Duration::from_secs(24 * 3600);
            let budget = self
                .shared
                .config
                .turn_timeout
                .unwrap_or(unbounded)
                .min(self.shared.drain.grace_left().unwrap_or(unbounded));
            let request = pb::ParentRequest {
                kind: Some(Request::Dump(pb::Dump {})),
                trace_parent: None,
            };
            let mut quiet = no_events;
            if let Ok(Ok(pb::ChildEvent {
                kind: Some(Event::DumpResult(result)),
                ..
            })) = timeout(budget, active.turn_raw(&request, &mut quiet)).await
            {
                match self.shared.config.dump_keys.sign(&result.state) {
                    Some(envelope) => {
                        self.shared.metrics.dump(DumpOp::Signed);
                        dump = Some(envelope);
                    }
                    None => logline::warn(
                        "shutdown_dump_too_large",
                        &[("client", &self.client), ("bytes", &result.state.len())],
                    ),
                }
            }
        }
        checkout.take();
        let _ = self.send(&events::shutdown(dump)).await;
        self.out.close(CLOSE_GOING_AWAY, texts::CLOSE_SHUTTING_DOWN).await;
        SessionEnd::Drained
    }

    async fn keepalive_tick(&mut self) -> Result<(), SessionEnd> {
        let Some(interval) = self.shared.config.keepalive else {
            self.keepalive_at = None;
            return Ok(());
        };
        let now_ms = u64::try_from(self.started.elapsed().as_millis())
            .unwrap_or(u64::MAX)
            .max(1);
        if let Some(sent) = self.ping_sent_ms {
            if self.last_pong_ms.load(Ordering::Relaxed) >= sent {
                self.ping_sent_ms = None;
            } else if now_ms.saturating_sub(sent) >= u64::try_from(interval.as_millis()).unwrap_or(u64::MAX) {
                self.shared.metrics.timeout(TimeoutKind::Keepalive);
                return Err(SessionEnd::Timeout(TimeoutKind::Keepalive));
            }
        }
        if self.ping_sent_ms.is_none() {
            self.out.ping().await.map_err(|_| SessionEnd::WriteFailed)?;
            self.ping_sent_ms = Some(now_ms);
        }
        self.keepalive_at = Some(Instant::now() + interval);
        Ok(())
    }

    async fn send(&self, event: &pb::ChildEvent) -> Result<(), SessionEnd> {
        self.out.frame(event).await.map_err(|_| SessionEnd::WriteFailed)
    }

    fn arm_idle(&mut self) {
        self.idle_deadline = self.shared.config.idle_timeout.map(|limit| Instant::now() + limit);
    }

    async fn violation(&self, reason: &str) -> SessionEnd {
        self.out.close(CLOSE_POLICY, reason).await;
        SessionEnd::Violation(reason.to_owned())
    }

    async fn timed_out(&self, kind: TimeoutKind) -> SessionEnd {
        let config = &self.shared.config;
        self.shared.metrics.timeout(kind);
        let reason = match kind {
            TimeoutKind::Idle => config.idle_timeout.map(texts::close_idle),
            TimeoutKind::Session => config.session_timeout.map(texts::close_session),
            TimeoutKind::Turn => config.turn_timeout.map(texts::close_turn),
            TimeoutKind::Keepalive => None,
        };
        if let Some(reason) = reason {
            self.out.close(CLOSE_POLICY, reason).await;
        }
        SessionEnd::Timeout(kind)
    }

    async fn drain_dropped(&self) -> SessionEnd {
        self.out.close(CLOSE_GOING_AWAY, texts::CLOSE_SHUTTING_DOWN).await;
        SessionEnd::DrainDropped
    }

    fn record_end(&mut self, end: &SessionEnd) {
        let outcome = match end {
            SessionEnd::ClientClosed => Outcome::Closed,
            SessionEnd::Violation(_) | SessionEnd::WorkerFailed | SessionEnd::WriteFailed => Outcome::Error,
            SessionEnd::Timeout(_) => Outcome::Timeout,
            SessionEnd::Drained => Outcome::Drained,
            SessionEnd::DrainDropped => Outcome::DrainDropped,
        };
        self.shared.metrics.outcome(outcome);
        if let Some(mut span) = self.span.take() {
            match end {
                SessionEnd::Timeout(kind) => span.event("timeout", kind.as_str()),
                SessionEnd::Violation(reason) => span.event("violation", reason),
                _ => {}
            }
            span.end(outcome);
        }
        let duration_ms = self.started.elapsed().as_millis();
        logline::info(
            "session_end",
            &[
                ("client", &self.client),
                ("outcome", &outcome.as_str()),
                ("detail", &format!("{end:?}")),
                ("duration_ms", &duration_ms),
            ],
        );
    }
}
