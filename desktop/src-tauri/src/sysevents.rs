//! System events that make every tunnel suspect: the computer woke from
//! sleep, or the network changed (#156).
//!
//! Both are detected by polling, with no Objective-C bridge:
//!
//! - **Wake**: the monotonic clock (`Instant`, `mach_absolute_time` /
//!   `CLOCK_UPTIME_RAW` on macOS, `CLOCK_MONOTONIC` on Linux) stops while the
//!   machine sleeps and the wall clock does not, so a wall-clock step much
//!   larger than the monotonic one between two polls means the machine slept.
//! - **Network change**: the set of non-loopback interface addresses that
//!   are up (`getifaddrs`) differs from the previous poll — Wi-Fi joined or
//!   left, a VPN up or down, a new DHCP lease. Windows has no
//!   `getifaddrs`; there the fingerprint is the source address of the
//!   default IPv4 and IPv6 routes (#260).
//!
//! The tunnel thread asks a [`SystemEventSource`] for events on every tick;
//! tests use their own source to fire events at will.

use std::collections::BTreeSet;
use std::time::{Duration, Instant, SystemTime};

/// Something that invalidates established tunnels.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SystemEvent {
    Wake,
    NetworkChanged,
}

/// Where the tunnel manager learns about wake and network changes.
pub trait SystemEventSource: Send {
    /// The events since the previous call.
    fn poll(&mut self) -> Vec<SystemEvent>;
}

/// A wall-clock step this much larger than the monotonic step between two
/// polls counts as a sleep.
pub const SLEEP_GAP: Duration = Duration::from_secs(10);

/// Whether the machine slept between two polls.
pub fn slept(wall_elapsed: Duration, mono_elapsed: Duration) -> bool {
    wall_elapsed > mono_elapsed + SLEEP_GAP
}

/// The up, non-loopback interface addresses, as `name=address` strings.
#[cfg(unix)]
pub fn interface_fingerprint() -> BTreeSet<String> {
    let mut out = BTreeSet::new();
    let mut head: *mut libc::ifaddrs = std::ptr::null_mut();
    // SAFETY: getifaddrs fills a list freed below with freeifaddrs; every
    // pointer read is checked for null and only read while the list lives.
    unsafe {
        if libc::getifaddrs(&mut head) != 0 {
            return out;
        }
        let mut cur = head;
        while !cur.is_null() {
            let ifa = &*cur;
            cur = ifa.ifa_next;
            let flags = ifa.ifa_flags;
            if flags & (libc::IFF_UP as libc::c_uint) == 0
                || flags & (libc::IFF_LOOPBACK as libc::c_uint) != 0
                || ifa.ifa_addr.is_null()
                || ifa.ifa_name.is_null()
            {
                continue;
            }
            let name = std::ffi::CStr::from_ptr(ifa.ifa_name)
                .to_string_lossy()
                .into_owned();
            let family = i32::from((*ifa.ifa_addr).sa_family);
            let addr = if family == libc::AF_INET {
                let sin = &*(ifa.ifa_addr as *const libc::sockaddr_in);
                std::net::Ipv4Addr::from(u32::from_be(sin.sin_addr.s_addr)).to_string()
            } else if family == libc::AF_INET6 {
                let sin6 = &*(ifa.ifa_addr as *const libc::sockaddr_in6);
                let ip = std::net::Ipv6Addr::from(sin6.sin6_addr.s6_addr);
                // Link-local addresses come and go with every interface
                // flap without a route change; ignore them.
                if ip.segments()[0] & 0xffc0 == 0xfe80 {
                    continue;
                }
                ip.to_string()
            } else {
                continue;
            };
            out.insert(format!("{name}={addr}"));
        }
        libc::freeifaddrs(head);
    }
    out
}

/// Windows: the local addresses the system routes outbound IPv4 and IPv6
/// traffic from, as `route4=…`/`route6=…`. Connecting a UDP socket only
/// asks the routing table; nothing is sent. A network change (Wi-Fi, VPN,
/// a new lease) changes the source address, which is what matters to a
/// tunnel. Wake is still seen through the clocks where they differ; where
/// the monotonic clock keeps counting through sleep, a wake that keeps the
/// address goes unnoticed and the tunnel's health probe catches it.
#[cfg(not(unix))]
pub fn interface_fingerprint() -> BTreeSet<String> {
    use std::net::UdpSocket;
    let mut out = BTreeSet::new();
    // TEST-NET addresses: routable in form, never contacted.
    for (tag, bind, peer) in [
        ("route4", "0.0.0.0:0", "192.0.2.1:9"),
        ("route6", "[::]:0", "[2001:db8::1]:9"),
    ] {
        let local = UdpSocket::bind(bind)
            .and_then(|s| s.connect(peer).map(|()| s))
            .and_then(|s| s.local_addr());
        if let Ok(a) = local {
            out.insert(format!("{tag}={}", a.ip()));
        }
    }
    out
}

type Fingerprint = Box<dyn FnMut() -> BTreeSet<String> + Send>;
type Clock = Box<dyn FnMut() -> (SystemTime, Instant) + Send>;

/// The polling detector.
pub struct PollingSystemEvents {
    clock: Clock,
    fingerprint: Fingerprint,
    last: Option<(SystemTime, Instant, BTreeSet<String>)>,
}

impl PollingSystemEvents {
    /// The real clocks and interfaces.
    pub fn new() -> Self {
        Self::with(
            Box::new(|| (SystemTime::now(), Instant::now())),
            Box::new(interface_fingerprint),
        )
    }

    /// Injected clocks and interface listing (tests).
    pub fn with(clock: Clock, fingerprint: Fingerprint) -> Self {
        PollingSystemEvents {
            clock,
            fingerprint,
            last: None,
        }
    }
}

impl Default for PollingSystemEvents {
    fn default() -> Self {
        Self::new()
    }
}

impl SystemEventSource for PollingSystemEvents {
    fn poll(&mut self) -> Vec<SystemEvent> {
        let (wall, mono) = (self.clock)();
        let fp = (self.fingerprint)();
        let mut out = Vec::new();
        if let Some((lw, lm, lfp)) = &self.last {
            // A wall clock set backwards is not a sleep.
            let wall_el = wall.duration_since(*lw).unwrap_or_default();
            if slept(wall_el, mono.saturating_duration_since(*lm)) {
                out.push(SystemEvent::Wake);
            }
            if *lfp != fp {
                out.push(SystemEvent::NetworkChanged);
            }
        }
        self.last = Some((wall, mono, fp));
        out
    }
}

/// A source that fires what it is told to (tests, and the menu's
/// Reconnect All).
#[derive(Default)]
pub struct ManualEvents {
    rx: Option<std::sync::mpsc::Receiver<SystemEvent>>,
}

impl ManualEvents {
    pub fn new() -> (std::sync::mpsc::Sender<SystemEvent>, Self) {
        let (tx, rx) = std::sync::mpsc::channel();
        (tx, ManualEvents { rx: Some(rx) })
    }
}

impl SystemEventSource for ManualEvents {
    fn poll(&mut self) -> Vec<SystemEvent> {
        self.rx
            .as_ref()
            .map(|rx| rx.try_iter().collect())
            .unwrap_or_default()
    }
}

/// Several sources as one.
pub struct Combined(pub Vec<Box<dyn SystemEventSource>>);

impl SystemEventSource for Combined {
    fn poll(&mut self) -> Vec<SystemEvent> {
        let mut out = Vec::new();
        for s in &mut self.0 {
            for e in s.poll() {
                if !out.contains(&e) {
                    out.push(e);
                }
            }
        }
        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::{Arc, Mutex};

    #[test]
    fn a_wall_clock_jump_past_the_monotonic_clock_is_a_wake() {
        assert!(!slept(Duration::from_secs(3), Duration::from_secs(3)));
        assert!(!slept(Duration::from_secs(12), Duration::from_secs(3)));
        assert!(slept(Duration::from_secs(600), Duration::from_secs(3)));
    }

    #[test]
    fn polling_source_reports_wake_and_network_changes() {
        let t0w = SystemTime::now();
        let t0m = Instant::now();
        let step = Arc::new(Mutex::new((t0w, t0m)));
        let addrs = Arc::new(Mutex::new(BTreeSet::from(["en0=192.168.1.5".to_string()])));
        let (s2, a2) = (Arc::clone(&step), Arc::clone(&addrs));
        let mut src = PollingSystemEvents::with(
            Box::new(move || *s2.lock().unwrap()),
            Box::new(move || a2.lock().unwrap().clone()),
        );
        assert!(src.poll().is_empty(), "the first poll is the baseline");
        *step.lock().unwrap() = (t0w + Duration::from_secs(3), t0m + Duration::from_secs(3));
        assert!(src.poll().is_empty());
        // Asleep for an hour: the monotonic clock moved 3 s.
        *step.lock().unwrap() = (
            t0w + Duration::from_secs(3603),
            t0m + Duration::from_secs(6),
        );
        assert_eq!(src.poll(), vec![SystemEvent::Wake]);
        addrs.lock().unwrap().insert("utun3=10.8.0.2".into());
        *step.lock().unwrap() = (
            t0w + Duration::from_secs(3606),
            t0m + Duration::from_secs(9),
        );
        assert_eq!(src.poll(), vec![SystemEvent::NetworkChanged]);
    }

    #[test]
    fn interface_fingerprint_has_no_loopback() {
        assert!(interface_fingerprint()
            .iter()
            .all(|a| !a.ends_with("=127.0.0.1") && !a.ends_with("=::1")));
    }

    #[test]
    fn manual_and_combined_sources_fire_on_demand() {
        let (tx, m) = ManualEvents::new();
        let (tx2, m2) = ManualEvents::new();
        let mut c = Combined(vec![Box::new(m), Box::new(m2)]);
        assert!(c.poll().is_empty());
        tx.send(SystemEvent::Wake).unwrap();
        tx2.send(SystemEvent::Wake).unwrap();
        tx2.send(SystemEvent::NetworkChanged).unwrap();
        assert_eq!(
            c.poll(),
            vec![SystemEvent::Wake, SystemEvent::NetworkChanged]
        );
    }
}
