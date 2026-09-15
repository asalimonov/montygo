use std::{
    io::{Read, Write},
    net::{TcpStream, ToSocketAddrs},
    process::ExitCode,
    time::Duration,
};

const PROBE_TIMEOUT: Duration = Duration::from_secs(2);

/// Plain HTTP/1.1 GET over a TcpStream; exit 0 on status 200.
pub fn probe(url: Option<&str>) -> ExitCode {
    let default = format!(
        "http://127.0.0.1:{}/health",
        std::env::var("MONTY_SERVER_PORT").unwrap_or_else(|_| "8000".to_owned())
    );
    let url = url.unwrap_or(&default);
    match get_status(url) {
        Ok(200) => ExitCode::SUCCESS,
        Ok(status) => {
            eprintln!("monty-server probe: {url} returned {status}");
            ExitCode::FAILURE
        }
        Err(err) => {
            eprintln!("monty-server probe: {url}: {err}");
            ExitCode::FAILURE
        }
    }
}

fn get_status(url: &str) -> Result<u16, String> {
    let rest = url
        .strip_prefix("http://")
        .ok_or_else(|| "only http:// URLs are supported".to_owned())?;
    let (authority, path) = rest
        .split_once('/')
        .map_or((rest, "/".to_owned()), |(a, p)| (a, format!("/{p}")));
    let addr = authority
        .to_socket_addrs()
        .map_err(|err| err.to_string())?
        .next()
        .ok_or_else(|| "address did not resolve".to_owned())?;
    let mut stream = TcpStream::connect_timeout(&addr, PROBE_TIMEOUT).map_err(|err| err.to_string())?;
    stream
        .set_read_timeout(Some(PROBE_TIMEOUT))
        .map_err(|err| err.to_string())?;
    stream
        .set_write_timeout(Some(PROBE_TIMEOUT))
        .map_err(|err| err.to_string())?;
    write!(
        stream,
        "GET {path} HTTP/1.1\r\nHost: {authority}\r\nConnection: close\r\n\r\n"
    )
    .map_err(|err| err.to_string())?;
    let mut head = [0u8; 64];
    let n = stream.read(&mut head).map_err(|err| err.to_string())?;
    let line = std::str::from_utf8(&head[..n]).map_err(|err| err.to_string())?;
    line.split_whitespace()
        .nth(1)
        .and_then(|code| code.parse().ok())
        .ok_or_else(|| "malformed HTTP response".to_owned())
}
