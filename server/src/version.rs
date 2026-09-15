pub const SERVER_VERSION: &str = env!("CARGO_PKG_VERSION");
/// Full upstream SHA baked into the dump MAC. Moves with `proto/PROTO_REV`.
pub const MONTY_REV: &str = "f8acf4fa8fff78dfd11dc5a2042e4fdf0ab36c28";

#[cfg(test)]
mod tests {
    use super::MONTY_REV;

    #[test]
    fn monty_rev_matches_lockfile() {
        let lock = std::fs::read_to_string(concat!(env!("CARGO_MANIFEST_DIR"), "/Cargo.lock")).expect("Cargo.lock");
        let source = lock
            .split("[[package]]")
            .find(|pkg| pkg.contains("name = \"monty-pool\""))
            .and_then(|pkg| pkg.lines().find(|line| line.starts_with("source = ")))
            .expect("monty-pool source");
        let sha = source.trim_end_matches('"').rsplit('#').next().expect("sha");
        assert_eq!(sha, MONTY_REV);
    }
}
