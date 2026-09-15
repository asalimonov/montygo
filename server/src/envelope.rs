use hmac::{Hmac, Mac};
use monty_proto::MAX_FRAME_LEN;
use sha2::{Digest, Sha256};

use crate::version::MONTY_REV;

pub const MAGIC: [u8; 4] = *b"MTYD";
pub const VERSION: u8 = 1;
pub const HEADER_LEN: usize = 41;
pub const MIN_KEY_LEN: usize = 16;

type HmacSha256 = Hmac<Sha256>;

pub struct DumpKeys {
    current: Key,
    previous: Option<Key>,
}

struct Key {
    secret: Vec<u8>,
    id: [u8; 4],
}

#[derive(Debug, PartialEq, Eq)]
pub enum VerifyError {
    TooShort,
    BadMagic,
    UnsupportedVersion(u8),
    UnknownKey,
    BadMac,
}

impl std::fmt::Display for VerifyError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::TooShort => f.write_str("too short"),
            Self::BadMagic => f.write_str("bad magic"),
            Self::UnsupportedVersion(v) => write!(f, "unsupported version {v}"),
            Self::UnknownKey => f.write_str("unknown key"),
            Self::BadMac => f.write_str("bad mac"),
        }
    }
}

impl std::fmt::Debug for DumpKeys {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("DumpKeys")
            .field("previous", &self.previous.is_some())
            .finish_non_exhaustive()
    }
}

impl Key {
    fn new(secret: &[u8]) -> Self {
        let digest = Sha256::digest(secret);
        Self {
            secret: secret.to_vec(),
            id: [digest[0], digest[1], digest[2], digest[3]],
        }
    }

    fn mac(&self) -> HmacSha256 {
        let mut mac = HmacSha256::new_from_slice(&self.secret).expect("HMAC accepts any key length");
        mac.update(&MAGIC);
        mac.update(&[VERSION]);
        mac.update(&self.id);
        mac.update(MONTY_REV.as_bytes());
        mac
    }
}

impl DumpKeys {
    pub fn new(current: &[u8], previous: Option<&[u8]>) -> Result<Self, String> {
        if current.len() < MIN_KEY_LEN {
            return Err(format!("--dump-key must be at least {MIN_KEY_LEN} bytes"));
        }
        if let Some(previous) = previous
            && (previous.len() < MIN_KEY_LEN || previous == current)
        {
            return Err(format!(
                "--dump-key-previous must be at least {MIN_KEY_LEN} bytes and differ from --dump-key"
            ));
        }
        Ok(Self {
            current: Key::new(current),
            previous: previous.map(Key::new),
        })
    }

    /// Wraps a raw worker dump; `None` when the envelope would exceed the frame limit.
    pub fn sign(&self, state: &[u8]) -> Option<Vec<u8>> {
        if state.len() + HEADER_LEN > MAX_FRAME_LEN as usize {
            return None;
        }
        let mut mac = self.current.mac();
        mac.update(state);
        let tag = mac.finalize().into_bytes();
        let mut out = Vec::with_capacity(HEADER_LEN + state.len());
        out.extend_from_slice(&MAGIC);
        out.push(VERSION);
        out.extend_from_slice(&self.current.id);
        out.extend_from_slice(&tag);
        out.extend_from_slice(state);
        Some(out)
    }

    /// Returns the raw dump when the envelope verifies under the current or previous key.
    pub fn verify<'a>(&self, envelope: &'a [u8]) -> Result<&'a [u8], VerifyError> {
        if envelope.len() < HEADER_LEN {
            return Err(VerifyError::TooShort);
        }
        if envelope[0..4] != MAGIC {
            return Err(VerifyError::BadMagic);
        }
        if envelope[4] != VERSION {
            return Err(VerifyError::UnsupportedVersion(envelope[4]));
        }
        let id = &envelope[5..9];
        let key = if id == self.current.id {
            &self.current
        } else if let Some(previous) = self.previous.as_ref().filter(|k| id == k.id) {
            previous
        } else {
            return Err(VerifyError::UnknownKey);
        };
        let state = &envelope[HEADER_LEN..];
        let mut mac = key.mac();
        mac.update(state);
        mac.verify_slice(&envelope[9..HEADER_LEN])
            .map_err(|_| VerifyError::BadMac)?;
        Ok(state)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const KEY: &[u8] = b"0123456789abcdef-current";
    const OLD: &[u8] = b"0123456789abcdef-previous";

    #[test]
    fn round_trip() {
        let keys = DumpKeys::new(KEY, None).unwrap();
        let env = keys.sign(b"state").unwrap();
        assert_eq!(env.len(), HEADER_LEN + 5);
        assert_eq!(&env[..4], b"MTYD");
        assert_eq!(keys.verify(&env), Ok(&b"state"[..]));
    }

    #[test]
    fn tampering_any_region_fails() {
        let keys = DumpKeys::new(KEY, None).unwrap();
        let env = keys.sign(b"some dump state").unwrap();
        for (i, want) in [
            (0, VerifyError::BadMagic),
            (4, VerifyError::UnsupportedVersion(VERSION ^ 1)),
            (6, VerifyError::UnknownKey),
            (20, VerifyError::BadMac),
            (HEADER_LEN + 3, VerifyError::BadMac),
        ] {
            let mut bad = env.clone();
            bad[i] ^= 1;
            assert_eq!(keys.verify(&bad), Err(want), "byte {i}");
        }
    }

    #[test]
    fn previous_key_verifies_and_unknown_key_fails() {
        let old = DumpKeys::new(OLD, None).unwrap();
        let env = old.sign(b"x").unwrap();
        let rotated = DumpKeys::new(KEY, Some(OLD)).unwrap();
        assert_eq!(rotated.verify(&env), Ok(&b"x"[..]));
        let fresh = DumpKeys::new(KEY, None).unwrap();
        assert_eq!(fresh.verify(&env), Err(VerifyError::UnknownKey));
    }

    #[test]
    fn short_input_fails() {
        let keys = DumpKeys::new(KEY, None).unwrap();
        assert_eq!(keys.verify(b"MTYD"), Err(VerifyError::TooShort));
        assert_eq!(keys.verify(b""), Err(VerifyError::TooShort));
    }

    #[test]
    fn key_rules() {
        assert!(DumpKeys::new(b"short", None).is_err());
        assert!(DumpKeys::new(KEY, Some(KEY)).is_err());
        assert!(DumpKeys::new(KEY, Some(b"short")).is_err());
    }
}
