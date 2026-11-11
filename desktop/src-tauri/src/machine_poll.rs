//! The machine-health thread: reads the daemons' model-server lists, probes
//! the loopback servers and reads the GPU, off the refresh thread and off
//! the main thread. The refresh loop hands it the local daemons it read
//! ([`MachinePoller::submit`], never blocking); a round runs on each
//! hand-off and at least every [`LIST_INTERVAL`], and each part is
//! throttled on its own: lists every [`LIST_INTERVAL`], each server's
//! probe and the GPU every [`crate::machine_health::PROBE_INTERVAL`].
//! Every read is bounded: the daemon reads by the client's timeout, a probe
//! by [`PROBE_TIMEOUT`], `nvidia-smi` by [`GPU_TIMEOUT`] (killed when it
//! overruns). The tray and the rail read the result
//! ([`MachinePoller::menu_lines`], [`MachinePoller::panel`]) under a lock no
//! read holds.

use std::sync::mpsc::{self, Receiver, RecvTimeoutError, Sender};
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};
use std::time::{Duration, Instant};

use crate::client::{fetch_model_servers, probe_model_server};
use crate::gpu::{
    status_from_nvidia_smi, status_without_nvidia_smi, GpuStatus, APPLE_SILICON, NVIDIA_SMI_ARGS,
};
use crate::machine_health::{
    HealthAlert, ListRead, MachineHealth, MachinePanel, LIST_INTERVAL, PROBE_TIMEOUT,
};
use crate::sessions::SessionsJob;
use crate::shellpath::{find_in_path, Tools};

/// Bounds one `nvidia-smi` run.
pub const GPU_TIMEOUT: Duration = Duration::from_secs(5);

fn lock<T>(m: &Mutex<T>) -> MutexGuard<'_, T> {
    m.lock().unwrap_or_else(PoisonError::into_inner)
}

/// The machine-health state and the hand-off to its thread.
#[derive(Default)]
pub struct MachinePoller {
    state: Mutex<MachineHealth>,
    jobs: Mutex<Option<Sender<Vec<SessionsJob>>>>,
}

impl MachinePoller {
    /// Starts the thread. `tools` gives the PATH `nvidia-smi` is looked up
    /// in (the login shell's, resolved once by the app); `on_change` runs
    /// on the thread after a round that changed what the menu shows, with
    /// the notifications to show.
    pub fn start(
        self: &Arc<Self>,
        tools: impl Fn() -> Tools + Send + 'static,
        on_change: impl Fn(Vec<HealthAlert>) + Send + 'static,
    ) -> std::io::Result<()> {
        let (tx, rx) = mpsc::channel();
        *lock(&self.jobs) = Some(tx);
        let me = Arc::clone(self);
        std::thread::Builder::new()
            .name("itervox-machine".into())
            .spawn(move || me.run(&rx, &tools, &on_change))?;
        Ok(())
    }

    /// Hands the thread the local daemons of this refresh.
    pub fn submit(&self, jobs: Vec<SessionsJob>) {
        if let Some(tx) = lock(&self.jobs).as_ref() {
            let _ = tx.send(jobs);
        }
    }

    /// The tray's "Model servers" lines; empty when no project uses one.
    pub fn menu_lines(&self) -> Vec<String> {
        lock(&self.state).menu_lines()
    }

    /// The rail's panel; `None` when no project uses a model server.
    pub fn panel(&self) -> Option<MachinePanel> {
        lock(&self.state).panel()
    }

    fn run(
        &self,
        rx: &Receiver<Vec<SessionsJob>>,
        tools: &dyn Fn() -> Tools,
        on_change: &dyn Fn(Vec<HealthAlert>),
    ) {
        let mut jobs: Vec<SessionsJob> = Vec::new();
        let mut listed: Option<Instant> = None;
        loop {
            match rx.recv_timeout(LIST_INTERVAL) {
                Ok(j) => {
                    jobs = j;
                    // Only the newest hand-off matters.
                    while let Ok(j) = rx.try_recv() {
                        jobs = j;
                    }
                }
                Err(RecvTimeoutError::Timeout) => {}
                Err(RecvTimeoutError::Disconnected) => return,
            }
            let before = self.menu_lines();
            let now = Instant::now();
            if listed.is_none_or(|t| now.saturating_duration_since(t) >= LIST_INTERVAL) {
                let reads = read_lists(&jobs);
                lock(&self.state).apply_lists(reads);
                listed = Some(now);
            }
            let due = lock(&self.state).due_probes(now);
            let results = probe_all(&due);
            let mut alerts = Vec::new();
            {
                let mut st = lock(&self.state);
                let now = Instant::now();
                for (url, answered) in results {
                    alerts.extend(st.apply_probe(&url, answered, now));
                }
            }
            if lock(&self.state).gpu_due(Instant::now()) {
                let gpu = read_gpu(&tools());
                lock(&self.state).set_gpu(gpu, Instant::now());
            }
            if !alerts.is_empty() || self.menu_lines() != before {
                on_change(alerts);
            }
        }
    }
}

/// Reads every daemon's list in parallel; a failed read keeps that
/// project's last list ([`MachineHealth::apply_lists`]).
fn read_lists(jobs: &[SessionsJob]) -> Vec<ListRead> {
    std::thread::scope(|s| {
        let handles: Vec<_> = jobs
            .iter()
            .map(|j| {
                s.spawn(move || ListRead {
                    key: j.key.clone(),
                    name: j.name.clone(),
                    rows: fetch_model_servers(&j.url, j.token.as_deref())
                        .map_err(|e| e.to_string()),
                })
            })
            .collect();
        handles.into_iter().filter_map(|h| h.join().ok()).collect()
    })
}

/// Probes every URL in parallel, each bounded by [`PROBE_TIMEOUT`].
fn probe_all(urls: &[String]) -> Vec<(String, bool)> {
    std::thread::scope(|s| {
        let handles: Vec<_> = urls
            .iter()
            .map(|u| s.spawn(move || (u.clone(), probe_model_server(u, PROBE_TIMEOUT).is_ok())))
            .collect();
        handles.into_iter().filter_map(|h| h.join().ok()).collect()
    })
}

/// `nvidia-smi` when it is on the tools' PATH, bounded by [`GPU_TIMEOUT`];
/// otherwise Apple silicon's integrated GPU or "not reported".
fn read_gpu(tools: &Tools) -> GpuStatus {
    match find_in_path("nvidia-smi", &tools.path_env) {
        Some(program) => {
            let args: Vec<String> = NVIDIA_SMI_ARGS.iter().map(|a| a.to_string()).collect();
            status_from_nvidia_smi(crate::remote::run_bounded(
                &program,
                &args,
                tools,
                GPU_TIMEOUT,
            ))
        }
        None => status_without_nvidia_smi(APPLE_SILICON),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    /// A fake `nvidia-smi` on the tools' PATH is run with the query
    /// arguments and parsed; without one the GPU is Apple silicon's or
    /// not reported.
    #[cfg(unix)]
    #[test]
    fn reads_the_gpu_through_nvidia_smi_on_the_path() {
        use std::os::unix::fs::PermissionsExt;
        let dir = tempfile::tempdir().unwrap();
        let exe = dir.path().join("nvidia-smi");
        std::fs::write(
            &exe,
            "#!/bin/sh\n[ \"$1\" = \"--query-gpu=name,utilization.gpu,memory.used,memory.total\" ] || exit 9\necho 'NVIDIA GeForce RTX 4090, 37, 10445, 24564'\n",
        )
        .unwrap();
        std::fs::set_permissions(&exe, std::fs::Permissions::from_mode(0o755)).unwrap();
        let tools = Tools {
            path_env: dir.path().as_os_str().to_os_string(),
            ssh: None,
            gcloud: None,
            path_source: "inherited",
        };
        assert_eq!(
            crate::gpu::gpu_lines(&read_gpu(&tools)),
            vec!["GPU: NVIDIA GeForce RTX 4090 · 37% · 10.2/24.0 GB"]
        );
        let none = tempfile::tempdir().unwrap();
        let empty = Tools {
            path_env: none.path().as_os_str().to_os_string(),
            ..tools
        };
        assert_eq!(read_gpu(&empty), status_without_nvidia_smi(APPLE_SILICON));
    }
}
