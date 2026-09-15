use std::time::Duration;

use axum::extract::ws::{CloseFrame, Message, WebSocket};
use bytes::Bytes;
use futures_util::{SinkExt, stream::SplitSink};
use monty_proto::pb;
use tokio::{sync::mpsc, time::timeout};

use crate::events;

pub const CLOSE_NORMAL: u16 = 1000;
pub const CLOSE_GOING_AWAY: u16 = 1001;
pub const CLOSE_POLICY: u16 = 1008;
pub const CLOSE_INTERNAL: u16 = 1011;

const CLOSE_WRITE_BUDGET: Duration = Duration::from_secs(1);

pub enum Outbound {
    Frame(Bytes),
    Ping,
    Close { code: u16, reason: String },
}

/// Owns the sink. Ends after a Close, a write error, or when every sender is gone.
pub async fn writer(mut sink: SplitSink<WebSocket, Message>, mut rx: mpsc::Receiver<Outbound>) {
    while let Some(message) = rx.recv().await {
        let sent = match message {
            Outbound::Frame(bytes) => sink.send(Message::Binary(bytes)).await,
            Outbound::Ping => sink.send(Message::Ping(Bytes::new())).await,
            Outbound::Close { code, reason } => {
                let frame = CloseFrame {
                    code,
                    reason: reason.into(),
                };
                let _ = timeout(CLOSE_WRITE_BUDGET, sink.send(Message::Close(Some(frame)))).await;
                break;
            }
        };
        if sent.is_err() {
            break;
        }
    }
    let _ = timeout(CLOSE_WRITE_BUDGET, sink.close()).await;
}

#[derive(Debug)]
pub struct SendFailed;

#[derive(Clone)]
pub struct OutboundTx(mpsc::Sender<Outbound>);

impl OutboundTx {
    pub fn new(tx: mpsc::Sender<Outbound>) -> Self {
        Self(tx)
    }

    pub async fn frame(&self, event: &pb::ChildEvent) -> Result<(), SendFailed> {
        let bytes = events::encode(event).map_err(|_| SendFailed)?;
        self.bytes(bytes).await
    }

    pub async fn bytes(&self, bytes: Bytes) -> Result<(), SendFailed> {
        self.0.send(Outbound::Frame(bytes)).await.map_err(|_| SendFailed)
    }

    pub async fn ping(&self) -> Result<(), SendFailed> {
        self.0.send(Outbound::Ping).await.map_err(|_| SendFailed)
    }

    pub async fn close(&self, code: u16, reason: impl Into<String>) {
        let _ = self
            .0
            .send(Outbound::Close {
                code,
                reason: reason.into(),
            })
            .await;
    }
}
