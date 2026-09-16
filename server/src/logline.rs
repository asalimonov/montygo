use std::{
    fmt::{Display, Write as _},
    io::Write as _,
    time::{SystemTime, UNIX_EPOCH},
};

pub fn info(event: &str, fields: &[(&str, &dyn Display)]) {
    log("info", event, fields);
}

pub fn warn(event: &str, fields: &[(&str, &dyn Display)]) {
    log("warn", event, fields);
}

pub fn error(event: &str, fields: &[(&str, &dyn Display)]) {
    log("error", event, fields);
}

/// One logfmt line on stderr. Server events never go through tracing; the
/// subscriber telemetry installs carries only OpenTelemetry's own warnings.
fn log(level: &str, event: &str, fields: &[(&str, &dyn Display)]) {
    let millis = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| d.as_millis());
    let mut line = format!("ts_ms={millis} level={level} event={event}");
    for (key, value) in fields {
        let value = value.to_string();
        if value.is_empty() || value.contains([' ', '"', '=']) {
            let _ = write!(line, " {key}={value:?}");
        } else {
            let _ = write!(line, " {key}={value}");
        }
    }
    line.push('\n');
    let _ = std::io::stderr().lock().write_all(line.as_bytes());
}
