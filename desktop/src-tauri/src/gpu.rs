//! The GPU line of the "Model servers" section: what a local model server
//! is running on. NVIDIA GPUs are read with `nvidia-smi` when it is on the
//! PATH (the login shell's PATH, `shellpath.rs`); Apple silicon always has
//! an integrated GPU, known at build time, but its load has no cheap and
//! reliable source, so only its presence is shown. Anything else is "not
//! reported". The daemon's own machine status (CPU, memory, disk) is in the
//! dashboard (`internal/sysmon`), not here.

/// The arguments `nvidia-smi` is run with: one CSV line per GPU, no header,
/// no units.
pub const NVIDIA_SMI_ARGS: &[&str] = &[
    "--query-gpu=name,utilization.gpu,memory.used,memory.total",
    "--format=csv,noheader,nounits",
];

/// One GPU as `nvidia-smi` reports it. A value it does not support
/// (`[N/A]`, `[Not Supported]`) is `None`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct GpuReading {
    pub name: String,
    pub utilization_pct: Option<u32>,
    pub memory_used_mib: Option<u64>,
    pub memory_total_mib: Option<u64>,
}

/// The machine's GPU, as last read.
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub enum GpuStatus {
    /// Not read yet.
    #[default]
    Unknown,
    /// `nvidia-smi` answered with at least one GPU.
    Nvidia(Vec<GpuReading>),
    /// Apple silicon: an integrated GPU whose load is not read.
    AppleIntegrated,
    /// No source, or the source failed; the reason is for the tooltip.
    NotReported(String),
}

/// A number field of `nvidia-smi`'s CSV: an integer, or a float rounded
/// (some drivers print `12.0`); `None` for `[N/A]` and the like.
fn number(field: &str) -> Option<u64> {
    let f = field.trim();
    if let Ok(n) = f.parse::<u64>() {
        return Some(n);
    }
    f.parse::<f64>()
        .ok()
        .filter(|v| v.is_finite() && *v >= 0.0)
        .map(|v| v.round() as u64)
}

/// Whether `field` is nvidia-smi's way of saying "no value" (`[N/A]`,
/// `[Not Supported]`, `N/A`).
fn not_available(field: &str) -> bool {
    let f = field.trim();
    (f.starts_with('[') && f.ends_with(']')) || f.eq_ignore_ascii_case("n/a")
}

/// Parses `nvidia-smi --query-gpu=name,utilization.gpu,memory.used,
/// memory.total --format=csv,noheader,nounits`. Lines that do not have the
/// four fields, or whose three numbers are neither numbers nor "no value",
/// are skipped; a name may itself hold commas, so the numbers are read from
/// the right.
pub fn parse_nvidia_smi(out: &str) -> Vec<GpuReading> {
    out.lines()
        .filter_map(|line| {
            let fields: Vec<&str> = line.split(',').collect();
            if fields.len() < 4 {
                return None;
            }
            let (name, nums) = fields.split_at(fields.len() - 3);
            let name = name.join(",").trim().to_string();
            if name.is_empty() || !nums.iter().all(|f| number(f).is_some() || not_available(f)) {
                return None;
            }
            Some(GpuReading {
                name,
                utilization_pct: number(nums[0]).filter(|p| *p <= 100).map(|p| p as u32),
                memory_used_mib: number(nums[1]),
                memory_total_mib: number(nums[2]),
            })
        })
        .collect()
}

/// The status after an `nvidia-smi` run: its output, or why it failed.
pub fn status_from_nvidia_smi(result: Result<Vec<u8>, String>) -> GpuStatus {
    match result {
        Ok(out) => {
            let gpus = parse_nvidia_smi(&String::from_utf8_lossy(&out));
            if gpus.is_empty() {
                GpuStatus::NotReported("nvidia-smi listed no GPU".into())
            } else {
                GpuStatus::Nvidia(gpus)
            }
        }
        Err(e) => GpuStatus::NotReported(format!("nvidia-smi: {e}")),
    }
}

/// The status when `nvidia-smi` is not on the PATH: Apple silicon's
/// integrated GPU, or not reported.
pub fn status_without_nvidia_smi(apple_silicon: bool) -> GpuStatus {
    if apple_silicon {
        GpuStatus::AppleIntegrated
    } else {
        GpuStatus::NotReported("no nvidia-smi on the PATH".into())
    }
}

/// Whether this build runs on Apple silicon, which always has an
/// integrated GPU.
pub const APPLE_SILICON: bool = cfg!(all(target_os = "macos", target_arch = "aarch64"));

fn gib(mib: u64) -> String {
    format!("{:.1}", mib as f64 / 1024.0)
}

/// One GPU's line: `GPU: NVIDIA RTX 4090 · 37% · 10.2/24.0 GB`.
fn reading_line(g: &GpuReading) -> String {
    let mut parts = vec![format!("GPU: {}", g.name)];
    if let Some(p) = g.utilization_pct {
        parts.push(format!("{p}%"));
    }
    match (g.memory_used_mib, g.memory_total_mib) {
        (Some(u), Some(t)) => parts.push(format!("{}/{} GB", gib(u), gib(t))),
        (None, Some(t)) => parts.push(format!("{} GB", gib(t))),
        _ => {}
    }
    parts.join(" · ")
}

/// The GPU lines of the menu and the rail: one per NVIDIA GPU, or one line
/// saying what is known.
pub fn gpu_lines(status: &GpuStatus) -> Vec<String> {
    match status {
        GpuStatus::Unknown => vec!["GPU: checking…".into()],
        GpuStatus::Nvidia(gpus) => gpus.iter().map(reading_line).collect(),
        GpuStatus::AppleIntegrated => {
            vec!["GPU: Apple silicon (integrated) · load not reported".into()]
        }
        GpuStatus::NotReported(_) => vec!["GPU: not reported".into()],
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_one_line_per_gpu() {
        let out = "NVIDIA GeForce RTX 4090, 37, 10445, 24564\nNVIDIA A100-SXM4-80GB, 0, 1, 81920\n";
        assert_eq!(
            parse_nvidia_smi(out),
            vec![
                GpuReading {
                    name: "NVIDIA GeForce RTX 4090".into(),
                    utilization_pct: Some(37),
                    memory_used_mib: Some(10445),
                    memory_total_mib: Some(24564),
                },
                GpuReading {
                    name: "NVIDIA A100-SXM4-80GB".into(),
                    utilization_pct: Some(0),
                    memory_used_mib: Some(1),
                    memory_total_mib: Some(81920),
                },
            ]
        );
    }

    #[test]
    fn keeps_unsupported_values_and_commas_in_names() {
        let out = "Tesla K80, rev 2, [N/A], [Not Supported], 11441\r\n";
        assert_eq!(
            parse_nvidia_smi(out),
            vec![GpuReading {
                name: "Tesla K80, rev 2".into(),
                utilization_pct: None,
                memory_used_mib: None,
                memory_total_mib: Some(11441),
            }]
        );
        // A float is rounded; a percentage over 100 is dropped.
        let g = &parse_nvidia_smi("X, 250, 12.6, 100.0")[0];
        assert_eq!(g.utilization_pct, None);
        assert_eq!(g.memory_used_mib, Some(13));
        assert_eq!(g.memory_total_mib, Some(100));
    }

    #[test]
    fn empty_output_lists_nothing() {
        assert!(parse_nvidia_smi("").is_empty());
        assert!(parse_nvidia_smi("\n\n").is_empty());
        assert_eq!(
            status_from_nvidia_smi(Ok(Vec::new())),
            GpuStatus::NotReported("nvidia-smi listed no GPU".into())
        );
    }

    #[test]
    fn garbage_is_skipped() {
        let out = "NVIDIA-SMI has failed because it couldn't communicate with the NVIDIA driver.\n\
                   a, b, c, d\n, 1, 2, 3\nGood GPU, 5, 6, 7\n\u{0}\u{1}, x\n";
        let gpus = parse_nvidia_smi(out);
        assert_eq!(gpus.len(), 1);
        assert_eq!(gpus[0].name, "Good GPU");
        assert!(
            parse_nvidia_smi("-1, -2, -3, -4").is_empty(),
            "negative numbers are garbage"
        );
        assert_eq!(
            status_from_nvidia_smi(Ok(b"\xff\xfe not csv".to_vec())),
            GpuStatus::NotReported("nvidia-smi listed no GPU".into())
        );
        assert_eq!(
            status_from_nvidia_smi(Err("did not finish within 5s".into())),
            GpuStatus::NotReported("nvidia-smi: did not finish within 5s".into())
        );
    }

    #[test]
    fn lines_say_what_is_known() {
        let status =
            status_from_nvidia_smi(Ok(b"NVIDIA GeForce RTX 4090, 37, 10445, 24564\n".to_vec()));
        assert_eq!(
            gpu_lines(&status),
            vec!["GPU: NVIDIA GeForce RTX 4090 · 37% · 10.2/24.0 GB"]
        );
        let partial = GpuStatus::Nvidia(vec![GpuReading {
            name: "K80".into(),
            utilization_pct: None,
            memory_used_mib: None,
            memory_total_mib: Some(11441),
        }]);
        assert_eq!(gpu_lines(&partial), vec!["GPU: K80 · 11.2 GB"]);
        assert_eq!(
            gpu_lines(&status_without_nvidia_smi(true)),
            vec!["GPU: Apple silicon (integrated) · load not reported"]
        );
        assert_eq!(
            gpu_lines(&status_without_nvidia_smi(false)),
            vec!["GPU: not reported"]
        );
        assert_eq!(gpu_lines(&GpuStatus::Unknown), vec!["GPU: checking…"]);
    }
}
