//! Supervisor tokens: generation and storage.
//!
//! The app starts each daemon with a fresh `ITERVOX_SUPERVISOR_TOKEN` and
//! keeps it so it can call that daemon's API and open its dashboard later,
//! also after the app restarts (the daemons outlive it). On macOS the token
//! lives in the login Keychain (service [`KEYCHAIN_SERVICE`], account = the
//! project's absolute WORKFLOW.md path); elsewhere in memory only, which is
//! enough for tests and Linux development.

use std::collections::HashMap;
use std::sync::Mutex;

/// Keychain service name for the supervisor tokens.
pub const KEYCHAIN_SERVICE: &str = "dev.itervox.desktop.supervisor-token";

/// Bounds the daemon enforces on `ITERVOX_SUPERVISOR_TOKEN`
/// (cmd/itervox/supervisor_token.go).
pub const MIN_TOKEN_LEN: usize = 16;
pub const MAX_TOKEN_LEN: usize = 512;

/// Where tokens are kept, keyed by project (absolute WORKFLOW.md path).
pub trait TokenStore: Send + Sync {
    fn get(&self, project: &str) -> Result<Option<String>, String>;
    fn set(&self, project: &str, token: &str) -> Result<(), String>;
    fn delete(&self, project: &str) -> Result<(), String>;
}

/// Mirrors the daemon's check: 16–512 printable ASCII characters, no spaces.
pub fn validate_token(tok: &str) -> Result<(), String> {
    if !(MIN_TOKEN_LEN..=MAX_TOKEN_LEN).contains(&tok.len()) {
        return Err(format!(
            "supervisor token must be {MIN_TOKEN_LEN}-{MAX_TOKEN_LEN} characters"
        ));
    }
    if !tok.bytes().all(|c| (0x21..=0x7e).contains(&c)) {
        return Err("supervisor token must be printable ASCII without spaces".into());
    }
    Ok(())
}

/// A fresh token: 32 random bytes from the OS, hex-encoded (64 characters).
pub fn generate_token() -> Result<String, String> {
    let mut buf = [0u8; 32];
    getrandom::fill(&mut buf).map_err(|e| format!("no OS randomness: {e}"))?;
    Ok(buf.iter().map(|b| format!("{b:02x}")).collect())
}

/// In-memory store (non-macOS, tests).
#[derive(Default)]
pub struct MemoryTokenStore {
    tokens: Mutex<HashMap<String, String>>,
}

impl TokenStore for MemoryTokenStore {
    fn get(&self, project: &str) -> Result<Option<String>, String> {
        Ok(lock(&self.tokens).get(project).cloned())
    }
    fn set(&self, project: &str, token: &str) -> Result<(), String> {
        lock(&self.tokens).insert(project.to_string(), token.to_string());
        Ok(())
    }
    fn delete(&self, project: &str) -> Result<(), String> {
        lock(&self.tokens).remove(project);
        Ok(())
    }
}

fn lock<T>(m: &Mutex<T>) -> std::sync::MutexGuard<'_, T> {
    m.lock().unwrap_or_else(std::sync::PoisonError::into_inner)
}

/// The OS keychain through the `keyring` crate.
pub struct KeyringTokenStore {
    service: String,
}

impl KeyringTokenStore {
    pub fn new(service: &str) -> Self {
        Self {
            service: service.to_string(),
        }
    }

    fn entry(&self, project: &str) -> Result<keyring::Entry, String> {
        keyring::Entry::new(&self.service, project).map_err(|e| format!("keychain: {e}"))
    }
}

impl TokenStore for KeyringTokenStore {
    fn get(&self, project: &str) -> Result<Option<String>, String> {
        match self.entry(project)?.get_password() {
            Ok(t) => Ok(Some(t)),
            Err(keyring::Error::NoEntry) => Ok(None),
            Err(e) => Err(format!("keychain: {e}")),
        }
    }
    fn set(&self, project: &str, token: &str) -> Result<(), String> {
        self.entry(project)?
            .set_password(token)
            .map_err(|e| format!("keychain: {e}"))
    }
    fn delete(&self, project: &str) -> Result<(), String> {
        match self.entry(project)?.delete_credential() {
            Ok(()) | Err(keyring::Error::NoEntry) => Ok(()),
            Err(e) => Err(format!("keychain: {e}")),
        }
    }
}

/// Caches another store in memory, so the poll loop does not ask the
/// Keychain every few seconds. Misses are cached too, until a `set`.
pub struct CachedTokenStore<S: TokenStore> {
    inner: S,
    cache: Mutex<HashMap<String, Option<String>>>,
}

impl<S: TokenStore> CachedTokenStore<S> {
    pub fn new(inner: S) -> Self {
        Self {
            inner,
            cache: Mutex::default(),
        }
    }
}

impl<S: TokenStore> TokenStore for CachedTokenStore<S> {
    fn get(&self, project: &str) -> Result<Option<String>, String> {
        if let Some(v) = lock(&self.cache).get(project) {
            return Ok(v.clone());
        }
        let v = self.inner.get(project)?;
        lock(&self.cache).insert(project.to_string(), v.clone());
        Ok(v)
    }
    fn set(&self, project: &str, token: &str) -> Result<(), String> {
        self.inner.set(project, token)?;
        lock(&self.cache).insert(project.to_string(), Some(token.to_string()));
        Ok(())
    }
    fn delete(&self, project: &str) -> Result<(), String> {
        self.inner.delete(project)?;
        lock(&self.cache).insert(project.to_string(), None);
        Ok(())
    }
}

/// The platform's store: the Keychain on macOS, memory elsewhere.
pub fn platform_store() -> Box<dyn TokenStore> {
    if cfg!(target_os = "macos") {
        Box::new(CachedTokenStore::new(KeyringTokenStore::new(
            KEYCHAIN_SERVICE,
        )))
    } else {
        Box::new(MemoryTokenStore::default())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicUsize, Ordering};

    #[test]
    fn generated_tokens_are_valid_and_distinct() {
        let a = generate_token().unwrap();
        let b = generate_token().unwrap();
        assert_eq!(a.len(), 64);
        assert!(validate_token(&a).is_ok());
        assert_ne!(a, b);
    }

    #[test]
    fn validate_token_mirrors_the_daemon_rule() {
        assert!(validate_token("short").is_err());
        assert!(validate_token(&"a".repeat(513)).is_err());
        assert!(validate_token("has a space in the middle").is_err());
        assert!(validate_token(&"a".repeat(16)).is_ok());
    }

    #[test]
    fn memory_store_round_trip() {
        let s = MemoryTokenStore::default();
        assert_eq!(s.get("/p/WORKFLOW.md").unwrap(), None);
        s.set("/p/WORKFLOW.md", "tok").unwrap();
        assert_eq!(s.get("/p/WORKFLOW.md").unwrap().as_deref(), Some("tok"));
        s.delete("/p/WORKFLOW.md").unwrap();
        assert_eq!(s.get("/p/WORKFLOW.md").unwrap(), None);
    }

    struct Counting {
        inner: MemoryTokenStore,
        gets: AtomicUsize,
    }
    impl TokenStore for Counting {
        fn get(&self, p: &str) -> Result<Option<String>, String> {
            self.gets.fetch_add(1, Ordering::SeqCst);
            self.inner.get(p)
        }
        fn set(&self, p: &str, t: &str) -> Result<(), String> {
            self.inner.set(p, t)
        }
        fn delete(&self, p: &str) -> Result<(), String> {
            self.inner.delete(p)
        }
    }

    #[test]
    fn cached_store_asks_the_backing_store_once() {
        let c = CachedTokenStore::new(Counting {
            inner: MemoryTokenStore::default(),
            gets: AtomicUsize::new(0),
        });
        assert_eq!(c.get("p").unwrap(), None);
        assert_eq!(c.get("p").unwrap(), None);
        c.set("p", "tok").unwrap();
        assert_eq!(c.get("p").unwrap().as_deref(), Some("tok"));
        assert_eq!(c.inner.gets.load(Ordering::SeqCst), 1);
        c.delete("p").unwrap();
        assert_eq!(c.get("p").unwrap(), None);
    }
}
