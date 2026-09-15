use std::{path::PathBuf, sync::Arc, time::Duration};

use futures_util::{SinkExt, StreamExt};
use monty_proto::{decode_frame, encode_to_capped_vec, pb};
use monty_server::{
    app::{self, Server, StartOptions},
    config::Config,
    envelope::DumpKeys,
    limits::Ceilings,
    texts,
};
use monty_types::MontyObject;
use pb::{child_event::Kind as Event, parent_request::Kind as Request};
use tokio::{net::TcpStream, time::timeout};
use tokio_tungstenite::{MaybeTlsStream, WebSocketStream, connect_async, tungstenite::Message};

type Socket = WebSocketStream<MaybeTlsStream<TcpStream>>;

const KEY: &[u8] = b"server-integration-test-key";
const IO_BUDGET: Duration = Duration::from_secs(30);

fn monty_bin() -> Option<PathBuf> {
    let path = std::env::var_os("MONTY_BIN").map_or_else(
        || PathBuf::from(concat!(env!("CARGO_MANIFEST_DIR"), "/../../monty/target/debug/monty")),
        PathBuf::from,
    );
    path.is_file().then_some(path)
}

fn config(bin: PathBuf, max_sessions: usize) -> Config {
    Config {
        bind: "127.0.0.1:0".to_owned(),
        monty_bin: bin,
        max_sessions,
        max_sessions_per_client: None,
        idle_timeout: Some(Duration::from_secs(30)),
        keepalive: Some(Duration::from_secs(5)),
        session_timeout: None,
        turn_timeout: Some(Duration::from_secs(60)),
        drain_grace: Duration::from_secs(10),
        ceilings: Ceilings::from_args(60, 64, 1000),
        trust_forwarded_for: false,
        dump_keys: Arc::new(DumpKeys::new(KEY, None).unwrap()),
        otlp: None,
    }
}

async fn start(max_sessions: usize) -> Option<Server> {
    let Some(bin) = monty_bin() else {
        eprintln!("skipping: MONTY_BIN not found");
        return None;
    };
    let options = StartOptions {
        print_url: false,
        watch_signals: false,
    };
    Some(
        app::start(config(bin, max_sessions), None, options)
            .await
            .expect("start server"),
    )
}

async fn dial(server: &Server) -> Socket {
    let (socket, _) = connect_async(format!("ws://{}/", server.bound)).await.expect("dial");
    socket
}

async fn send(socket: &mut Socket, kind: Request) {
    let request = pb::ParentRequest {
        kind: Some(kind),
        trace_parent: None,
    };
    let body = encode_to_capped_vec(&request).unwrap();
    socket.send(Message::Binary(body.into())).await.expect("send");
}

enum Received {
    Event(Box<pb::ChildEvent>),
    Closed(Option<u16>),
}

async fn recv(socket: &mut Socket) -> Received {
    loop {
        match timeout(IO_BUDGET, socket.next()).await.expect("receive timed out") {
            Some(Ok(Message::Binary(data))) => {
                return Received::Event(Box::new(decode_frame(&data).expect("decode event")));
            }
            Some(Ok(Message::Close(frame))) => return Received::Closed(frame.map(|f| u16::from(f.code))),
            Some(Ok(_)) => {}
            Some(Err(_)) | None => return Received::Closed(None),
        }
    }
}

async fn event(socket: &mut Socket) -> pb::ChildEvent {
    match recv(socket).await {
        Received::Event(event) => *event,
        Received::Closed(code) => panic!("connection closed ({code:?}) while waiting for an event"),
    }
}

/// Reads past prints to the turn-ending event.
async fn turn_end(socket: &mut Socket) -> pb::ChildEvent {
    loop {
        let event = event(socket).await;
        if !matches!(event.kind, Some(Event::Print(_))) {
            return event;
        }
    }
}

fn configure(protocol_version: u32) -> Request {
    Request::Configure(pb::Configure {
        script_name: "main.py".to_owned(),
        protocol_version,
        ..Default::default()
    })
}

fn feed(code: &str) -> Request {
    Request::Feed(pb::Feed {
        code: code.to_owned(),
        inputs: vec![],
        skip_type_check: false,
        cwd: "/".to_owned(),
    })
}

fn complete(value: i64) -> Option<Event> {
    Some(Event::Complete(pb::Complete {
        value: Some(MontyObject::Int(value).into()),
    }))
}

async fn configured(server: &Server) -> Socket {
    let mut socket = dial(server).await;
    send(&mut socket, configure(3)).await;
    let ok = event(&mut socket).await;
    assert!(matches!(ok.kind, Some(Event::Ok(_))), "{ok:?}");
    socket
}

#[tokio::test]
async fn configure_reports_clamped_budget_and_feeds_stream_prints() {
    let Some(server) = start(4).await else { return };
    let mut socket = dial(&server).await;
    send(&mut socket, configure(3)).await;
    let ok = event(&mut socket).await;
    assert!(matches!(ok.kind, Some(Event::Ok(_))));
    assert_eq!(ok.max_duration_micros, Some(60_000_000));
    assert_eq!(ok.max_suspensions, Some(1000));

    send(&mut socket, feed("print('hi')\n1 + 1")).await;
    let print = event(&mut socket).await;
    assert!(matches!(print.kind, Some(Event::Print(_))), "{print:?}");
    assert_eq!(turn_end(&mut socket).await.kind, complete(2));
}

#[tokio::test]
async fn version_skew_is_fatal() {
    let Some(server) = start(4).await else { return };
    let mut socket = dial(&server).await;
    send(&mut socket, configure(2)).await;
    let fatal = event(&mut socket).await;
    let Some(Event::FatalError(fatal)) = fatal.kind else {
        panic!("expected FatalError, got {fatal:?}")
    };
    assert_eq!(
        fatal.message,
        "unsupported protocol version 2 (server supports protocol version 3, try updating to a newer client version)"
    );
    assert!(matches!(recv(&mut socket).await, Received::Closed(Some(1000) | None)));
}

#[tokio::test]
async fn dumps_are_signed_and_restore_on_a_new_connection() {
    let Some(server) = start(4).await else { return };
    let mut first = configured(&server).await;
    send(&mut first, feed("x = 41")).await;
    turn_end(&mut first).await;
    send(&mut first, Request::Dump(pb::Dump {})).await;
    let Some(Event::DumpResult(result)) = event(&mut first).await.kind else {
        panic!("expected DumpResult")
    };
    assert_eq!(&result.state[..4], b"MTYD");
    drop(first);

    let mut second = configured(&server).await;
    send(&mut second, Request::Load(pb::Load { state: result.state })).await;
    let loaded = event(&mut second).await;
    assert!(matches!(loaded.kind, Some(Event::Ok(_))), "{loaded:?}");
    send(&mut second, feed("x + 1")).await;
    assert_eq!(turn_end(&mut second).await.kind, complete(42));
}

#[tokio::test]
async fn invalid_dump_is_a_value_error_and_keeps_the_session() {
    let Some(server) = start(4).await else { return };
    let mut socket = configured(&server).await;
    send(
        &mut socket,
        Request::Load(pb::Load {
            state: b"forged".to_vec(),
        }),
    )
    .await;
    let Some(Event::Error(error)) = event(&mut socket).await.kind else {
        panic!("expected Error")
    };
    let exception = error.exception.expect("exception");
    let debug = format!("{exception:?}");
    assert!(debug.contains("ValueError"), "{debug}");
    assert!(debug.contains(texts::INVALID_DUMP), "{debug}");
    send(&mut socket, feed("1 + 2")).await;
    assert_eq!(turn_end(&mut socket).await.kind, complete(3));
}

#[tokio::test]
async fn lifecycle_requests_close_with_policy_violation() {
    let Some(server) = start(4).await else { return };
    let mut socket = configured(&server).await;
    send(&mut socket, Request::Reset(pb::Reset {})).await;
    assert!(matches!(recv(&mut socket).await, Received::Closed(Some(1008))));
}

#[tokio::test]
async fn first_request_must_be_configure() {
    let Some(server) = start(4).await else { return };
    let mut socket = dial(&server).await;
    send(&mut socket, feed("1")).await;
    assert!(matches!(recv(&mut socket).await, Received::Closed(Some(1008))));
}

#[tokio::test]
async fn drain_answers_the_next_request_with_a_signed_shutdown_dump() {
    let Some(server) = start(4).await else { return };
    let mut socket = configured(&server).await;
    send(&mut socket, feed("x = 1")).await;
    turn_end(&mut socket).await;
    server.begin_drain();
    send(&mut socket, feed("x")).await;
    let Some(Event::Shutdown(shutdown)) = event(&mut socket).await.kind else {
        panic!("expected ShutdownDump")
    };
    assert_eq!(&shutdown.dump.expect("dump")[..4], b"MTYD");
    assert!(matches!(recv(&mut socket).await, Received::Closed(Some(1001) | None)));
    drop(socket);
    timeout(IO_BUDGET, server.wait())
        .await
        .expect("drain finishes")
        .expect("serve");
}

#[tokio::test]
async fn full_server_rejects_upgrades_with_503() {
    let Some(server) = start(1).await else { return };
    let _held = configured(&server).await;
    let err = connect_async(format!("ws://{}/", server.bound))
        .await
        .expect_err("second dial");
    assert!(err.to_string().contains("503"), "{err}");
}
