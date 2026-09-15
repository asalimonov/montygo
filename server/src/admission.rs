use std::{
    collections::HashMap,
    sync::{Arc, Mutex, PoisonError},
};

use crate::metrics::Metrics;

pub struct Admission {
    max_sessions: usize,
    per_client: Option<usize>,
    state: Mutex<AdmissionState>,
    metrics: Arc<Metrics>,
}

#[derive(Default)]
struct AdmissionState {
    active: usize,
    by_client: HashMap<String, usize>,
}

#[derive(Debug, PartialEq, Eq)]
pub enum Rejection {
    Capacity,
    ClientQuota,
    Draining,
}

/// Holds one session slot; dropping it releases the slot.
pub struct SessionPermit {
    admission: Arc<Admission>,
    client: String,
}

impl Admission {
    pub fn new(max_sessions: usize, per_client: Option<usize>, metrics: Arc<Metrics>) -> Arc<Self> {
        Arc::new(Self {
            max_sessions,
            per_client,
            state: Mutex::new(AdmissionState::default()),
            metrics,
        })
    }

    pub fn try_admit(self: &Arc<Self>, client: String, draining: bool) -> Result<SessionPermit, Rejection> {
        if draining {
            return Err(Rejection::Draining);
        }
        let mut state = self.state.lock().unwrap_or_else(PoisonError::into_inner);
        if state.active >= self.max_sessions {
            return Err(Rejection::Capacity);
        }
        if let Some(limit) = self.per_client
            && state.by_client.get(&client).copied().unwrap_or(0) >= limit
        {
            return Err(Rejection::ClientQuota);
        }
        state.active += 1;
        *state.by_client.entry(client.clone()).or_insert(0) += 1;
        drop(state);
        self.metrics.sessions_active.inc();
        Ok(SessionPermit {
            admission: Arc::clone(self),
            client,
        })
    }

    pub fn active(&self) -> usize {
        self.state.lock().unwrap_or_else(PoisonError::into_inner).active
    }
}

impl SessionPermit {
    pub fn client(&self) -> &str {
        &self.client
    }
}

impl Drop for SessionPermit {
    fn drop(&mut self) {
        let admission = &self.admission;
        let mut state = admission.state.lock().unwrap_or_else(PoisonError::into_inner);
        state.active -= 1;
        if let Some(count) = state.by_client.get_mut(&self.client) {
            *count -= 1;
            if *count == 0 {
                state.by_client.remove(&self.client);
            }
        }
        drop(state);
        admission.metrics.sessions_active.dec();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn capacity_quota_and_release() {
        let admission = Admission::new(2, Some(1), Metrics::new());
        let a = admission.try_admit("a".into(), false).unwrap();
        assert_eq!(
            admission.try_admit("a".into(), false).err(),
            Some(Rejection::ClientQuota)
        );
        let b = admission.try_admit("b".into(), false).unwrap();
        assert_eq!(admission.try_admit("c".into(), false).err(), Some(Rejection::Capacity));
        drop(a);
        assert_eq!(admission.active(), 1);
        let _a2 = admission.try_admit("a".into(), false).unwrap();
        drop(b);
        assert_eq!(admission.active(), 1);
    }

    #[test]
    fn draining_rejects_and_no_quota_means_unbounded_per_client() {
        let admission = Admission::new(3, None, Metrics::new());
        assert_eq!(admission.try_admit("a".into(), true).err(), Some(Rejection::Draining));
        let _p: Vec<_> = (0..3)
            .map(|_| admission.try_admit("a".into(), false).unwrap())
            .collect();
        assert_eq!(admission.try_admit("a".into(), false).err(), Some(Rejection::Capacity));
    }
}
