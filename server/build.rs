fn main() {
    println!("cargo:rerun-if-env-changed=MONTY_SERVER_VERSION");
    let version = std::env::var("MONTY_SERVER_VERSION")
        .ok()
        .filter(|v| !v.is_empty())
        .unwrap_or_else(|| env!("CARGO_PKG_VERSION").to_owned());
    println!("cargo:rustc-env=MONTY_SERVER_VERSION={version}");
}
