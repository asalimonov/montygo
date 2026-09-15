# Wire protocol

The schema is vendored at `proto/monty/v1/monty.proto` (upstream rev in `proto/PROTO_REV`). The parent declares `protocol_version = 3` in `Configure`.

## Framing

- Subprocess and wasm workers use a 4-byte little-endian length prefix per message on stdio.
- WebSocket workers use one binary message per protocol message.
- A frame MUST NOT exceed 256 MiB. Oversize requests are rejected before any byte is written, so the stream stays in sync and a pending suspension stays answerable.
- The exchange is strictly alternating: one request, zero or more `Print` events, one turn-ending event.

## Codec

`internal/wire` encodes and decodes with `protowire` by hand instead of generated structs:

- Values decode straight into the `internal/value` model, with no intermediate protobuf tree.
- Decoding charges a 1 GiB resident-memory budget per frame (88 bytes per value node plus payload bytes). A worker that exceeds it is discarded.
- Semantic validation runs while decoding: date and time ranges, timedelta normalization, type origins, uuids, file handle modes.
- `repr` and `cycle` values are output-only; the encoder rejects them.
- The generated `montypb` package is the oracle for differential tests (`internal/wire/codec_test.go`).

## Value depth

Before sending, the pool checks nesting with upstream's per-shape costs (list-like 2, dict 3, class instance 4) against a budget of 97. A too-deep feed input fails host-side; a too-deep host return raises `RuntimeError: Max input depth exceeded` inside the sandbox.

## Exceptions

`wire.Exception.Render` reproduces monty's traceback Display: repeated frames collapse after three, carets use `~` only, and the summary line has no trailing newline.
