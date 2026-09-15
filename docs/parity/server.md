# Server parity with Full Monty

Full Monty is specified by upstream `docs/server.md`. `monty-server` follows its flags, defaults, admission status codes, limit semantics, drain protocol and security model. This page lists every deviation. The design is in `docs/architecture/server.md`.

## Deviations

| Full Monty (`../monty/docs/server.md`) | monty-server | Reason |
|---|---|---|
| closed source; `linux/amd64` image only | open source; `linux/amd64` and `linux/arm64` images, built locally with `make docker-build` | task scope |
| private dump signature format | `MTYD` v1 envelope with HMAC-SHA256 | the format is not public; dumps do not move between the two servers |
| `--logfire-token` / `LOGFIRE_TOKEN` | `--otlp-endpoint` / `OTEL_EXPORTER_OTLP_ENDPOINT` and `--otlp-protocol` / `OTEL_EXPORTER_OTLP_PROTOCOL` | generic OTLP export |
| — | only `http/protobuf` export exists. `--otlp-protocol grpc` fails at startup with `OTLP gRPC export is not supported; use --otlp-protocol http/protobuf` | exporters use blocking HTTP clients; no gRPC exporter is built |
| without a token, traces and logs are written to stderr | stderr carries one logfmt line per server event; spans are neither exported nor printed without an endpoint | — |
| traces carry the WebSocket handshake request headers | the connection span carries `client.address` and `user_agent.original`; `traceparent` sets its parent | no other header is recorded |
| no metrics endpoint documented | `GET /metrics` in OpenMetrics text | observability |
| — | `monty_server_sessions_total{outcome}` includes `drain_dropped` | separates sessions silent through `--drain-grace` from drained ones |
| a worker is "reset or replaced" between sessions | a fresh `monty subprocess` per session, never reused | isolation |
| server texts not public | close reasons, HTTP bodies and the info page are defined in `server/src/texts.rs`; the protocol version refusal and `PoolError` texts come verbatim from `monty-proto` and `monty-pool` | — |
| `GET /health` returns 200 while accepting | 200 while accepting; 503 `monty server is shutting down` while draining, on HTTP connections opened before SIGTERM | the listener closes on the first SIGTERM, so new connections are refused either way |
| — | `monty-server probe [--url URL]` subcommand | healthcheck in a `scratch` image |
| — | `GET /info`: JSON with the server version, `monty_rev`, `protocol_version` and the effective limits; montygo reads it with `FetchServerInfo` | clients size pools and detect drift before dialing |
| version `0.0.23` as the Monty release | the server reports the montygo build version from `scripts/version.sh`, stamped through `MONTY_SERVER_VERSION`; the Monty revision is `monty_rev` | one version per image, tied to the git tag |
| — | `--dump-key-previous` / `MONTY_SERVER_DUMP_KEY_PREVIOUS` | key rotation |
| — | a signed dump larger than the frame limit: `DumpResult` becomes `FatalError` with close 1011; `ShutdownDump` is sent without a dump and logged | not specified upstream |
| — | after `Load`, only the echoed `max_duration_micros` is checked against the ceiling; the worker does not echo a restored memory limit | a dump signed under a higher memory ceiling restores with that limit |
| — | no upgrade handshake timeout | slow upgrades are a documented risk only |
| reverse proxy to a CPython sandbox | not implemented | — |
