use bytes::Bytes;
use monty_proto::{FrameError, decode_frame, encode_to_capped_vec, pb};
use monty_types::{DEFAULT_MAX_SUSPENSIONS, ExcType, MontyException};

use pb::child_event::Kind;

pub fn encode(event: &pb::ChildEvent) -> Result<Bytes, FrameError> {
    encode_to_capped_vec(event).map(Bytes::from)
}

pub fn decode_request(bytes: &[u8]) -> Result<pb::ParentRequest, FrameError> {
    decode_frame(bytes)
}

fn event(kind: Kind) -> pb::ChildEvent {
    pb::ChildEvent {
        kind: Some(kind),
        ..Default::default()
    }
}

/// The acknowledgement of a Configure, carrying the effective budget as a child does.
pub fn ok(limits: &pb::ResourceLimits) -> pb::ChildEvent {
    pb::ChildEvent {
        max_duration_micros: limits.max_duration_micros,
        max_suspensions: Some(limits.max_suspensions.unwrap_or(DEFAULT_MAX_SUSPENSIONS as u64)),
        ..event(Kind::Ok(pb::Ok {}))
    }
}

pub fn error(exc_type: ExcType, message: &str) -> pb::ChildEvent {
    error_from(&MontyException::new(exc_type, Some(message.to_owned())))
}

pub fn error_from(exception: &MontyException) -> pb::ChildEvent {
    event(Kind::Error(pb::Error {
        exception: Some(exception.into()),
    }))
}

pub fn fatal(message: &str) -> pb::ChildEvent {
    event(Kind::FatalError(pb::FatalError {
        message: message.to_owned(),
    }))
}

pub fn shutdown(dump: Option<Vec<u8>>) -> pb::ChildEvent {
    event(Kind::Shutdown(pb::ShutdownDump { dump }))
}

pub fn is_suspension(event: &pb::ChildEvent) -> bool {
    matches!(
        event.kind,
        Some(Kind::FunctionCall(_) | Kind::OsCall(_) | Kind::NameLookup(_) | Kind::ResolveFutures(_))
    )
}

pub fn is_ok(event: &pb::ChildEvent) -> bool {
    matches!(event.kind, Some(Kind::Ok(_)))
}
