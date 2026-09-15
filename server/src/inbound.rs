use std::sync::{
    Arc,
    atomic::{AtomicU64, Ordering},
};

use axum::extract::ws::{Message, WebSocket};
use bytes::Bytes;
use futures_util::{StreamExt, stream::SplitStream};
use tokio::{sync::mpsc, time::Instant};

pub enum Inbound {
    Request(Bytes),
    Text,
    Closed,
}

/// Forwards binary messages, records pongs as milliseconds since `started`, and reports the end.
pub async fn reader(
    mut stream: SplitStream<WebSocket>,
    tx: mpsc::Sender<Inbound>,
    last_pong_ms: Arc<AtomicU64>,
    started: Instant,
) {
    loop {
        let item = match stream.next().await {
            Some(Ok(Message::Binary(bytes))) => Inbound::Request(bytes),
            Some(Ok(Message::Text(_))) => Inbound::Text,
            Some(Ok(Message::Pong(_))) => {
                let now = u64::try_from(started.elapsed().as_millis()).unwrap_or(u64::MAX);
                last_pong_ms.store(now, Ordering::Relaxed);
                continue;
            }
            Some(Ok(Message::Ping(_))) => continue,
            Some(Ok(Message::Close(_)) | Err(_)) | None => Inbound::Closed,
        };
        let closed = matches!(item, Inbound::Closed);
        if tx.send(item).await.is_err() || closed {
            return;
        }
    }
}
