//! `monty subprocess` built for wasm32-wasip1: framed protobuf requests on
//! stdin, framed events on stdout, driven by montygo's wazero transport.
mod subprocess;

use std::process::ExitCode;

#[global_allocator]
static ALLOC: monty_alloc::LimitedAllocator = monty_alloc::LimitedAllocator;

fn main() -> ExitCode {
    subprocess::run()
}
