use std::net::{IpAddr, SocketAddr};

use axum::http::HeaderMap;

/// Caller identity for the per-client quota.
pub fn client_id(peer: SocketAddr, headers: &HeaderMap, trust_forwarded_for: bool) -> String {
    if trust_forwarded_for
        && let Some(ip) = headers
            .get_all("x-forwarded-for")
            .iter()
            .next_back()
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.rsplit(',').next())
            .and_then(|entry| entry.trim().parse::<IpAddr>().ok())
    {
        return canonical(ip);
    }
    canonical(peer.ip())
}

fn canonical(ip: IpAddr) -> String {
    match ip {
        IpAddr::V6(v6) => v6.to_ipv4_mapped().map_or(IpAddr::V6(v6), IpAddr::V4).to_string(),
        IpAddr::V4(_) => ip.to_string(),
    }
}

#[cfg(test)]
mod tests {
    use axum::http::HeaderValue;

    use super::*;

    fn peer() -> SocketAddr {
        "10.0.0.7:5000".parse().unwrap()
    }

    #[test]
    fn peer_ip_without_trust() {
        let mut headers = HeaderMap::new();
        headers.insert("x-forwarded-for", HeaderValue::from_static("1.2.3.4"));
        assert_eq!(client_id(peer(), &headers, false), "10.0.0.7");
    }

    #[test]
    fn last_forwarded_entry_with_trust() {
        let mut headers = HeaderMap::new();
        headers.append("x-forwarded-for", HeaderValue::from_static("9.9.9.9"));
        headers.append("x-forwarded-for", HeaderValue::from_static("1.2.3.4, 5.6.7.8"));
        assert_eq!(client_id(peer(), &headers, true), "5.6.7.8");
    }

    #[test]
    fn invalid_forwarded_entry_falls_back() {
        let mut headers = HeaderMap::new();
        headers.insert("x-forwarded-for", HeaderValue::from_static("nonsense"));
        assert_eq!(client_id(peer(), &headers, true), "10.0.0.7");
    }

    #[test]
    fn ipv6_is_canonical() {
        let v6: SocketAddr = "[::ffff:10.1.2.3]:1".parse().unwrap();
        assert_eq!(client_id(v6, &HeaderMap::new(), false), "10.1.2.3");
        let mut headers = HeaderMap::new();
        headers.insert("x-forwarded-for", HeaderValue::from_static("2001:DB8::1"));
        assert_eq!(client_id(peer(), &headers, true), "2001:db8::1");
    }
}
