use std::{fmt::Display, time::Duration};

use crate::version::{MONTY_REV, SERVER_VERSION};

pub const INVALID_DUMP: &str = "invalid session dump signature";
pub const HTTP_CAPACITY: &str = "monty server at capacity";
pub const HTTP_CLIENT_QUOTA: &str = "too many sessions for this client";
pub const HTTP_DRAINING: &str = "monty server is shutting down";
pub const CLOSE_EXPECTED_CONFIGURE: &str = "expected Configure as the first request";
pub const CLOSE_ALREADY_CONFIGURED: &str = "session already configured";
pub const CLOSE_LIFECYCLE: &str = "lifecycle requests (Reset/Shutdown) are not accepted";
pub const CLOSE_TEXT_MESSAGE: &str = "text messages are not part of the protocol";
pub const CLOSE_EMPTY_REQUEST: &str = "request has no kind";
pub const CLOSE_RESTORED_OVER_LIMITS: &str = "restored session exceeds server limits";
pub const CLOSE_SHUTTING_DOWN: &str = "server is shutting down";

/// A WebSocket close reason MUST fit in 123 bytes.
const MAX_CLOSE_REASON: usize = 123;

pub fn close_malformed(err: &dyn Display) -> String {
    truncate(format!("malformed request frame: {err}"))
}

pub fn close_idle(limit: Duration) -> String {
    format!("idle timeout of {}s exceeded", limit.as_secs())
}

pub fn close_session(limit: Duration) -> String {
    format!("session timeout of {}s exceeded", limit.as_secs())
}

pub fn close_turn(limit: Duration) -> String {
    format!("turn timeout of {}s exceeded", limit.as_secs())
}

pub fn frame_too_large(len: usize, max: u32) -> String {
    format!("response frame of {len} bytes exceeds maximum of {max} bytes")
}

pub fn info_page(bound: &str) -> String {
    format!("monty-server {SERVER_VERSION} (monty {MONTY_REV})\nWebSocket endpoint: ws://{bound}/\n")
}

fn truncate(mut text: String) -> String {
    if text.len() > MAX_CLOSE_REASON {
        let mut end = MAX_CLOSE_REASON;
        while !text.is_char_boundary(end) {
            end -= 1;
        }
        text.truncate(end);
    }
    text
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn close_reasons_fit() {
        let long = "é".repeat(200);
        assert!(close_malformed(&long).len() <= MAX_CLOSE_REASON);
        assert_eq!(close_idle(Duration::from_secs(60)), "idle timeout of 60s exceeded");
    }
}
