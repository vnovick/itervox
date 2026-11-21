//! The configured terminal of "Open in Terminal" (#278): the optional
//! `terminal_command` preference in `projects.json`.
//!
//! Without it the app opens the system terminal exactly as
//! [`crate::terminal::launch`] does. With it, the app runs the operator's
//! own argument vector — never a shell string — in which one element is a
//! placeholder, used only as a whole element:
//!
//! - `{command}` is replaced by the attach command's words, as separate
//!   elements ([`crate::terminal::attach_argv`]: `tmux attach -t <name>`,
//!   the ssh form or the gcloud IAP form);
//! - `{script}` is replaced by the path of a one-shot script running that
//!   command (the `.command` file macOS's Terminal opens; on Linux the same
//!   script, written only when `{script}` is used), for apps that only open
//!   files. Windows has no such script: `{script}` is refused there.
//!
//! The program (the first element) is used as given: an absolute path, or
//! a name looked up on the PATH the app resolves for ssh and gcloud. There
//! is no detection and no table of known terminals: the setting says what
//! runs. A setting that does not validate is never run, not even in part:
//! the Sessions window shows why and the default terminal is used.

use std::path::Path;

use crate::platform::Os;
use crate::terminal::{command_file, Launch};

/// The placeholder for the attach command's words.
pub const COMMAND: &str = "{command}";
/// The placeholder for the one-shot script's path.
pub const SCRIPT: &str = "{script}";

/// At most this many elements, each at most [`MAX_ELEMENT`] bytes.
const MAX_ELEMENTS: usize = 64;
const MAX_ELEMENT: usize = 4096;

/// Which placeholder the command uses.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Placeholder {
    Command,
    Script,
}

/// A validated `terminal_command`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TerminalCommand {
    argv: Vec<String>,
    uses: Placeholder,
}

fn bad(why: &str) -> String {
    format!("The terminal command {why}.")
}

/// Validates a `terminal_command` value for `os`: a non-empty JSON array of
/// strings, the first not starting with `-` nor a placeholder, no empty
/// element and no control characters, placeholders only as whole elements,
/// and exactly one placeholder. On Windows `{script}` is refused, and so is
/// `;` (Windows Terminal splits its command line there).
pub fn parse(value: &serde_json::Value, os: Os) -> Result<TerminalCommand, String> {
    let items = value.as_array().ok_or_else(|| {
        bad("must be a JSON array of strings, for example [\"ghostty\", \"-e\", \"{command}\"]")
    })?;
    if items.is_empty() {
        return Err(bad("is empty"));
    }
    if items.len() > MAX_ELEMENTS {
        return Err(bad("has too many elements"));
    }
    let mut argv = Vec::with_capacity(items.len());
    for item in items {
        let s = item.as_str().ok_or_else(|| bad("must hold only strings"))?;
        if s.is_empty() {
            return Err(bad("holds an empty element"));
        }
        if s.len() > MAX_ELEMENT {
            return Err(bad("holds an element that is too long"));
        }
        if s.chars().any(char::is_control) {
            return Err(bad("holds a control character"));
        }
        if s != COMMAND && s != SCRIPT && (s.contains(COMMAND) || s.contains(SCRIPT)) {
            return Err(format!(
                "The terminal command uses a placeholder inside \"{s}\": {COMMAND} and {SCRIPT} must each be an element of their own."
            ));
        }
        if os == Os::Windows && s.contains(';') {
            return Err(bad(
                "holds a ';', which Windows Terminal reads as a command separator",
            ));
        }
        argv.push(s.to_string());
    }
    let first = &argv[0];
    if first.starts_with('-') {
        return Err(bad("must start with the program to run, not an option"));
    }
    if first == COMMAND || first == SCRIPT {
        return Err(bad("must start with the program to run, not a placeholder"));
    }
    let commands = argv.iter().filter(|a| *a == COMMAND).count();
    let scripts = argv.iter().filter(|a| *a == SCRIPT).count();
    let uses = match (commands, scripts) {
        (1, 0) => Placeholder::Command,
        (0, 1) => Placeholder::Script,
        (0, 0) => {
            return Err(format!(
                "The terminal command needs {COMMAND} (the attach command) or {SCRIPT} (a script that runs it)."
            ))
        }
        _ => {
            return Err(format!(
                "The terminal command must use exactly one of {COMMAND} and {SCRIPT}, once."
            ))
        }
    };
    if uses == Placeholder::Script && os == Os::Windows {
        return Err(format!(
            "{SCRIPT} is not available on Windows: use {COMMAND}, for example [\"wt.exe\", \"new-tab\", \"{COMMAND}\"]."
        ));
    }
    Ok(TerminalCommand { argv, uses })
}

/// Validates the text of the Sessions window's field: JSON, then
/// [`parse`]. Returns the normalized value to store with the command.
pub fn parse_text(text: &str, os: Os) -> Result<(serde_json::Value, TerminalCommand), String> {
    if text.len() > MAX_ELEMENTS * 64 {
        return Err(bad("is too long"));
    }
    let value: serde_json::Value = serde_json::from_str(text.trim()).map_err(|_| {
        bad("must be a JSON array of strings, for example [\"ghostty\", \"-e\", \"{command}\"]")
    })?;
    let cmd = parse(&value, os)?;
    Ok((serde_json::Value::from(cmd.argv.clone()), cmd))
}

/// The stored preference for `os`: `Ok(None)` when absent (the default
/// terminal), the command when it validates, else why not.
pub fn from_saved(
    value: Option<&serde_json::Value>,
    os: Os,
) -> Result<Option<TerminalCommand>, String> {
    value.map(|v| parse(v, os)).transpose()
}

/// The stored preference as the field shows it (compact JSON; empty when
/// absent).
pub fn field_text(value: Option<&serde_json::Value>) -> String {
    value
        .and_then(|v| serde_json::to_string(v).ok())
        .unwrap_or_default()
}

/// The last path component of `p` (either separator), without its
/// extension: `/usr/bin/ghostty` → `ghostty`, `C:\x\wezterm.exe` →
/// `wezterm`, `/Applications/iTerm.app` → `iTerm`.
fn stem(p: &str) -> &str {
    let name = p
        .trim_end_matches(['/', '\\'])
        .rsplit(['/', '\\'])
        .next()
        .unwrap_or(p);
    match name.rsplit_once('.') {
        Some((s, _)) if !s.is_empty() => s,
        _ => name,
    }
}

/// The button label for a terminal command: "Open in Terminal" without
/// one, else "Open in <name>" — the program's file name without extension,
/// or `<App>` for macOS's `open -a <App>`.
pub fn label(cmd: Option<&TerminalCommand>) -> String {
    let Some(cmd) = cmd else {
        return "Open in Terminal".into();
    };
    let a = &cmd.argv;
    let name = match (stem(&a[0]), a.get(1).map(String::as_str), a.get(2)) {
        ("open", Some("-a"), Some(app)) if app != COMMAND && app != SCRIPT => stem(app),
        (s, _, _) => s,
    };
    if name.is_empty() {
        "Open in Terminal".into()
    } else {
        format!("Open in {name}")
    }
}

impl TerminalCommand {
    pub fn argv(&self) -> &[String] {
        &self.argv
    }

    pub fn uses(&self) -> Placeholder {
        self.uses
    }

    /// The launch of this command for `attach` (the attach argv, already
    /// checked by [`crate::terminal::attach_argv`]): `{command}` becomes
    /// its words, `{script}` the path `script` of a one-shot script that
    /// runs it (written by the caller). The attach words get the same
    /// checks as the default terminal's: non-empty, no control
    /// characters, no `;` on Windows.
    pub fn launch(
        &self,
        os: Os,
        attach: &[String],
        script: Option<&Path>,
    ) -> Result<Launch, String> {
        if attach.is_empty() || attach[0].starts_with('-') {
            return Err("Nothing to run in the terminal.".into());
        }
        if attach.iter().any(|w| {
            w.is_empty()
                || w.chars().any(char::is_control)
                || (os == Os::Windows && w.contains(';'))
        }) {
            return Err("The terminal command holds an unusable argument.".into());
        }
        let mut args = Vec::with_capacity(self.argv.len() + attach.len());
        let mut file = None;
        for a in &self.argv[1..] {
            if a == COMMAND {
                args.extend(attach.iter().cloned());
            } else if a == SCRIPT {
                if os == Os::Windows {
                    return Err(format!("{SCRIPT} is not available on Windows."));
                }
                let path = script.ok_or("No place to write the terminal command file.")?;
                args.push(path.to_string_lossy().into_owned());
                file = Some((path.to_path_buf(), command_file(attach)));
            } else {
                args.push(a.clone());
            }
        }
        Ok(Launch {
            program: self.argv[0].clone(),
            args,
            script: file,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::remote::IapMode;
    use crate::terminal::{attach_argv, attach_host, AttachHost};
    use serde_json::json;
    use std::path::PathBuf;

    fn v(words: &[&str]) -> Vec<String> {
        words.iter().map(|s| s.to_string()).collect()
    }

    fn ok(value: serde_json::Value) -> TerminalCommand {
        parse(&value, Os::Linux).unwrap_or_else(|e| panic!("{value}: {e}"))
    }

    #[test]
    fn terminal_command_parses_good_commands() {
        for (value, uses) in [
            (json!(["ghostty", "-e", "{command}"]), Placeholder::Command),
            (
                json!(["wezterm", "start", "--", "{command}"]),
                Placeholder::Command,
            ),
            (json!(["kitty", "{command}"]), Placeholder::Command),
            (
                json!(["open", "-a", "iTerm", "{script}"]),
                Placeholder::Script,
            ),
            (
                json!([
                    "/Applications/kitty.app/Contents/MacOS/kitty",
                    "-o",
                    "macos_quit_when_last_window_closed=yes",
                    "{command}"
                ]),
                Placeholder::Command,
            ),
            (
                json!(["/opt/My Term/term", "{command}"]),
                Placeholder::Command,
            ),
        ] {
            assert_eq!(ok(value.clone()).uses(), uses, "{value}");
            assert!(parse(&value, Os::Mac).is_ok(), "{value}");
        }
        assert!(parse(&json!(["wt.exe", "new-tab", "{command}"]), Os::Windows).is_ok());
    }

    #[test]
    fn terminal_command_refuses_bad_commands() {
        for (value, needle) in [
            (json!([]), "empty"),
            (json!("ghostty -e {command}"), "JSON array"),
            (json!({"program": "ghostty"}), "JSON array"),
            (json!(["ghostty", 3, "{command}"]), "only strings"),
            (json!(["ghostty", null, "{command}"]), "only strings"),
            (json!(["-e", "{command}"]), "not an option"),
            (json!(["{command}"]), "not a placeholder"),
            (json!(["{script}", "x"]), "not a placeholder"),
            (json!(["", "{command}"]), "empty element"),
            (json!(["ghostty", "", "{command}"]), "empty element"),
            (json!(["ghostty", "-e\n", "{command}"]), "control character"),
            (json!(["ghos\u{7}tty", "{command}"]), "control character"),
            (json!(["ghostty", "-e", "{command}\t"]), "control character"),
            (
                json!(["sh", "-c", "exec {command}"]),
                "element of their own",
            ),
            (
                json!(["open", "-a", "iTerm", "--args={script}"]),
                "element of their own",
            ),
            (json!(["ghostty", "-e"]), "needs {command}"),
            (json!(["ghostty", "{command}", "{script}"]), "exactly one"),
            (json!(["ghostty", "{command}", "{command}"]), "exactly one"),
        ] {
            let e = parse(&value, Os::Linux).unwrap_err();
            assert!(e.contains(needle), "{value}: {e}");
        }
        assert!(parse(&json!(vec!["x"; 65]), Os::Linux).is_err());
        assert!(parse(&json!(["x".repeat(4097), "{command}"]), Os::Linux).is_err());
        // Windows: no script, no ';'.
        let e = parse(&json!(["x", "{script}"]), Os::Windows).unwrap_err();
        assert!(e.contains("not available on Windows"), "{e}");
        assert!(parse(&json!(["wt.exe", "new-tab", ";", "{command}"]), Os::Windows).is_err());
        assert!(parse(&json!(["wt.exe", "new-tab", ";", "{command}"]), Os::Linux).is_ok());

        // The field's text: JSON first.
        assert!(parse_text("ghostty -e {command}", Os::Linux)
            .unwrap_err()
            .contains("JSON array"));
        let (value, cmd) = parse_text(" [\"ghostty\",\"-e\",\"{command}\"] ", Os::Linux).unwrap();
        assert_eq!(value, json!(["ghostty", "-e", "{command}"]));
        assert_eq!(cmd.argv(), &v(&["ghostty", "-e", "{command}"])[..]);
    }

    /// `{command}` becomes the attach words as separate elements, for the
    /// local, ssh and IAP forms.
    #[test]
    fn terminal_command_expands_each_attach_form() {
        let ghostty = ok(json!(["ghostty", "-e", "{command}"]));

        let local = attach_argv(&AttachHost::Local, "itervox-abc123", None, None).unwrap();
        let l = ghostty.launch(Os::Linux, &local, None).unwrap();
        assert_eq!(l.program, "ghostty");
        assert_eq!(l.args, v(&["-e", "tmux", "attach", "-t", "itervox-abc123"]));
        assert_eq!(l.script, None);

        let ssh = AttachHost::Ssh {
            destination: "me@vm-1".into(),
        };
        let argv = attach_argv(&ssh, "itervox-0f9e8d", Some("/usr/bin/ssh"), None).unwrap();
        let wez = ok(json!(["wezterm", "start", "--", "{command}"]));
        let l = wez.launch(Os::Mac, &argv, None).unwrap();
        assert_eq!(l.program, "wezterm");
        assert_eq!(&l.args[..4], &v(&["start", "--", "/usr/bin/ssh", "-t"])[..]);
        assert_eq!(l.args.len(), 2 + argv.len());
        assert_eq!(&l.args[2..], &argv[..]);

        let iap = crate::remote::Transport::Iap {
            instance: "itervox-vm".into(),
            zone: "europe-west1-b".into(),
            project: "demo-project".into(),
            mode: IapMode::Ssh,
        };
        let host = attach_host(Os::Linux, Some(&iap)).unwrap();
        let argv = attach_argv(&host, "itervox-abc123", None, Some("/opt/gcloud")).unwrap();
        let kitty = ok(json!(["kitty", "{command}"]));
        let l = kitty.launch(Os::Linux, &argv, None).unwrap();
        assert_eq!(l.program, "kitty");
        assert_eq!(l.args, argv);
        assert!(l
            .args
            .contains(&"--command=tmux attach -t itervox-abc123".to_string()));

        // Windows Terminal with the attach words after new-tab.
        let wt = parse(&json!(["wt.exe", "new-tab", "{command}"]), Os::Windows).unwrap();
        let l = wt
            .launch(
                Os::Windows,
                &v(&["ssh", "-t", "--", "box", "tmux", "attach", "-t", "work"]),
                None,
            )
            .unwrap();
        assert_eq!(l.program, "wt.exe");
        assert_eq!(l.args[0], "new-tab");
        assert_eq!(l.args.len(), 9);

        // The attach words are checked as the default's are.
        for bad in [v(&[]), v(&["-x"]), v(&["tmux", ""]), v(&["tmux", "a\nb"])] {
            assert!(ghostty.launch(Os::Linux, &bad, None).is_err(), "{bad:?}");
        }
        assert!(wt.launch(Os::Windows, &v(&["ssh", "a;b"]), None).is_err());
    }

    /// `{script}` becomes the path of the one-shot script, which runs the
    /// attach command (each word quoted) and deletes itself.
    #[test]
    fn terminal_command_script_placeholder_writes_the_one_shot_script() {
        let iterm = ok(json!(["open", "-a", "iTerm", "{script}"]));
        let local = attach_argv(&AttachHost::Local, "itervox-abc123", None, None).unwrap();
        let path = PathBuf::from("/tmp/itervox-attach-1-2.command");
        let l = iterm.launch(Os::Mac, &local, Some(&path)).unwrap();
        assert_eq!(l.program, "open");
        assert_eq!(
            l.args,
            v(&["-a", "iTerm", "/tmp/itervox-attach-1-2.command"])
        );
        let (p, body) = l.script.unwrap();
        assert_eq!(p, path);
        assert_eq!(body, command_file(&local));
        assert!(body.contains("rm -f -- \"$0\"\n"));
        assert!(body.ends_with("exec tmux attach -t itervox-abc123\n"));
        // Linux builds the same script when asked to.
        let l = ok(json!(["myterm", "--run", "{script}"]))
            .launch(Os::Linux, &local, Some(Path::new("/t/x.sh")))
            .unwrap();
        assert_eq!(l.args, v(&["--run", "/t/x.sh"]));
        assert!(l.script.is_some());
        // No path, no launch.
        assert!(iterm.launch(Os::Mac, &local, None).is_err());
        // {command} writes no script.
        assert_eq!(
            ok(json!(["kitty", "{command}"]))
                .launch(Os::Mac, &local, Some(&path))
                .unwrap()
                .script,
            None
        );
    }

    /// An absent setting is the default terminal; an invalid one is
    /// reported and never run.
    #[test]
    fn terminal_command_falls_back_to_default_when_invalid() {
        assert_eq!(from_saved(None, Os::Linux), Ok(None));
        assert!(from_saved(Some(&json!(["kitty", "{command}"])), Os::Linux)
            .unwrap()
            .is_some());
        let bad = json!(["kitty", "-e"]);
        assert!(from_saved(Some(&bad), Os::Linux).is_err());
        assert_eq!(field_text(Some(&bad)), "[\"kitty\",\"-e\"]");
        assert_eq!(field_text(None), "");
        // The label falls back with it.
        assert_eq!(
            label(from_saved(Some(&bad), Os::Linux).ok().flatten().as_ref()),
            "Open in Terminal"
        );
    }

    #[test]
    fn terminal_command_label_names_the_program() {
        let l = |value: serde_json::Value| label(Some(&ok(value)));
        assert_eq!(label(None), "Open in Terminal");
        assert_eq!(l(json!(["ghostty", "-e", "{command}"])), "Open in ghostty");
        assert_eq!(
            l(json!(["/usr/bin/wezterm", "start", "--", "{command}"])),
            "Open in wezterm"
        );
        assert_eq!(
            l(json!(["open", "-a", "iTerm", "{script}"])),
            "Open in iTerm"
        );
        assert_eq!(
            l(json!([
                "/usr/bin/open",
                "-a",
                "/Applications/Ghostty.app",
                "{script}"
            ])),
            "Open in Ghostty"
        );
        assert_eq!(
            l(json!([
                "/Applications/kitty.app/Contents/MacOS/kitty",
                "{command}"
            ])),
            "Open in kitty"
        );
        assert_eq!(l(json!(["open", "{script}"])), "Open in open");
        assert_eq!(
            label(Some(
                &parse(
                    &json!([
                        r"C:\Program Files\WezTerm\wezterm.exe",
                        "start",
                        "--",
                        "{command}"
                    ]),
                    Os::Windows
                )
                .unwrap()
            )),
            "Open in wezterm"
        );
        assert_eq!(l(json!(["wt.exe", "new-tab", "{command}"])), "Open in wt");
    }
}
