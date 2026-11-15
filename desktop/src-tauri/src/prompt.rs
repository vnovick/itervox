//! Native prompts for the remote flows (#156) through `osascript`: a text
//! field (optionally hidden, for tokens) and a list to choose from. The app
//! is a menu-bar shell without its own UI pages; AppleScript dialogs are the
//! native macOS way to ask a question from one. This module builds the
//! scripts and reads their answers; `app_remote` runs them.
//!
//! Linux and Windows (#260) ask the same questions with their own native
//! dialogs: `zenity` (GNOME and most desktops) or `kdialog` (KDE) on Linux,
//! a Windows Forms dialog through PowerShell on Windows. Every helper
//! answers on stdout and exits 1 when the user cancels.

use std::ffi::OsStr;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

use crate::platform::Os;

/// The macOS script runner.
pub const OSASCRIPT: &str = "/usr/bin/osascript";

/// An AppleScript string literal.
pub fn applescript_string(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for c in s.chars() {
        match c {
            '\\' => out.push_str("\\\\"),
            '"' => out.push_str("\\\""),
            '\n' | '\r' => out.push_str("\\n"),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// A dialog with a text field; prints the text typed.
pub fn text_prompt_script(message: &str, default: &str, hidden: bool) -> String {
    format!(
        "set r to display dialog {} default answer {}{} buttons {{\"Cancel\", \"OK\"}} default button \"OK\" with title \"Itervox\"\nreturn text returned of r",
        applescript_string(message),
        applescript_string(default),
        if hidden { " with hidden answer" } else { "" }
    )
}

/// A list to choose from; prints the chosen items' 1-based indices, one per
/// line (empty when cancelled). Items are numbered so names containing
/// commas or newlines cannot be confused.
pub fn choose_script(
    prompt: &str,
    items: &[String],
    multiple: bool,
    preselect_all: bool,
) -> String {
    let labels: Vec<String> = items
        .iter()
        .enumerate()
        .map(|(i, s)| applescript_string(&format!("{}. {}", i + 1, s.replace('\n', " "))))
        .collect();
    let list = format!("{{{}}}", labels.join(", "));
    let defaults = if preselect_all {
        format!(" default items {list}")
    } else if let Some(first) = labels.first() {
        format!(" default items {{{first}}}")
    } else {
        String::new()
    };
    format!(
        "set r to choose from list {list} with title \"Itervox\" with prompt {}{}{}\nif r is false then return \"\"\nset AppleScript's text item delimiters to linefeed\nreturn r as text",
        applescript_string(prompt),
        if multiple { " with multiple selections allowed" } else { "" },
        defaults
    )
}

/// The 0-based indices in [`choose_script`]'s output.
pub fn parse_choice(out: &str, n: usize) -> Vec<usize> {
    let mut v: Vec<usize> = out
        .lines()
        .filter_map(|l| l.trim().split_once(". ")?.0.parse::<usize>().ok())
        .filter(|i| (1..=n).contains(i))
        .map(|i| i - 1)
        .collect();
    v.sort_unstable();
    v.dedup();
    v
}

/// Whether osascript's stderr is the user pressing Cancel (error -128).
pub fn is_cancel(stderr: &str) -> bool {
    stderr.contains("(-128)")
}

/// Runs a script; `Ok(None)` when the user cancelled.
pub fn run_script(script: &str) -> Result<Option<String>, String> {
    let out = Command::new(OSASCRIPT)
        .arg("-e")
        .arg(script)
        .stdin(Stdio::null())
        .output()
        .map_err(|e| format!("could not show a dialog: {e}"))?;
    let stderr = String::from_utf8_lossy(&out.stderr);
    if !out.status.success() {
        if is_cancel(&stderr) {
            return Ok(None);
        }
        return Err(crate::supervisor::last_line(&stderr));
    }
    let s = String::from_utf8_lossy(&out.stdout);
    Ok(Some(s.strip_suffix('\n').unwrap_or(&s).to_string()))
}

/// The first question of Add Remote….
pub fn reach_question() -> &'static str {
    if Os::current() == Os::Mac {
        "How does this Mac reach the host?"
    } else {
        "How does this computer reach the host?"
    }
}

/// Asks for a line of text; `None` when cancelled.
pub fn ask_text(message: &str, default: &str, hidden: bool) -> Result<Option<String>, String> {
    let out = match Os::current() {
        Os::Mac => run_script(&text_prompt_script(message, default, hidden))?,
        Os::Linux | Os::Windows => {
            let (program, args) = match helper()? {
                Helper::Zenity(p) => (p, zenity_text_args(message, default, hidden)),
                Helper::Kdialog(p) => (p, kdialog_text_args(message, default, hidden)),
                Helper::PowerShell(p) => (
                    p,
                    powershell_args(&powershell_text_script(message, default, hidden)),
                ),
            };
            run_helper(&program, &args)?
        }
    };
    Ok(out.map(|s| s.trim().to_string()))
}

/// Asks to choose from `items`; `None` when cancelled or nothing chosen.
pub fn ask_choice(
    prompt: &str,
    items: &[String],
    multiple: bool,
    preselect_all: bool,
) -> Result<Option<Vec<usize>>, String> {
    let out = match Os::current() {
        Os::Mac => run_script(&choose_script(prompt, items, multiple, preselect_all))?
            .map(|o| parse_choice(&o, items.len())),
        Os::Linux | Os::Windows => {
            let (program, args) = match helper()? {
                Helper::Zenity(p) => (
                    p,
                    zenity_choice_args(prompt, items, multiple, preselect_all),
                ),
                Helper::Kdialog(p) => (
                    p,
                    kdialog_choice_args(prompt, items, multiple, preselect_all),
                ),
                Helper::PowerShell(p) => (
                    p,
                    powershell_args(&powershell_choice_script(
                        prompt,
                        items,
                        multiple,
                        preselect_all,
                    )),
                ),
            };
            run_helper(&program, &args)?.map(|o| parse_indices(&o, items.len()))
        }
    };
    Ok(out.filter(|v| !v.is_empty()))
}

/// The dialog program of Linux or Windows.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Helper {
    Zenity(PathBuf),
    Kdialog(PathBuf),
    PowerShell(PathBuf),
}

/// What to tell a Linux user with neither dialog program.
pub const NO_DIALOG_HELPER: &str =
    "Install zenity (GNOME and most desktops) or kdialog (KDE) so Itervox can ask for remote details.";

/// The dialog program for `os`, looked up on `path`: on Linux `kdialog`
/// first in a KDE session (`desktop` is `$XDG_CURRENT_DESKTOP`), else
/// `zenity` first; on Windows PowerShell.
pub fn find_helper(os: Os, path: &OsStr, desktop: &str) -> Result<Helper, String> {
    let find = |n: &str| crate::shellpath::find_in_path(n, path);
    match os {
        Os::Windows => Ok(Helper::PowerShell(
            find("powershell").unwrap_or_else(|| PathBuf::from("powershell.exe")),
        )),
        Os::Linux | Os::Mac => {
            let kde = desktop.split(':').any(|d| d.eq_ignore_ascii_case("KDE"));
            let zenity = || find("zenity").map(Helper::Zenity);
            let kdialog = || find("kdialog").map(Helper::Kdialog);
            let found = if kde {
                kdialog().or_else(zenity)
            } else {
                zenity().or_else(kdialog)
            };
            found.ok_or_else(|| NO_DIALOG_HELPER.to_string())
        }
    }
}

fn helper() -> Result<Helper, String> {
    let path = std::env::var_os("PATH").unwrap_or_default();
    let desktop = std::env::var("XDG_CURRENT_DESKTOP").unwrap_or_default();
    find_helper(Os::current(), &path, &desktop)
}

/// `zenity --entry`: prints the text.
pub fn zenity_text_args(message: &str, default: &str, hidden: bool) -> Vec<String> {
    let mut a = vec![
        "--entry".to_string(),
        "--title=Itervox".into(),
        format!("--text={message}"),
        format!("--entry-text={default}"),
        "--width=480".into(),
    ];
    if hidden {
        a.push("--hide-text".into());
    }
    a
}

/// `zenity --list` with a check (or radio) column: prints the chosen rows'
/// 1-based numbers, one per line.
pub fn zenity_choice_args(
    prompt: &str,
    items: &[String],
    multiple: bool,
    preselect_all: bool,
) -> Vec<String> {
    let mut a = vec![
        "--list".to_string(),
        "--title=Itervox".into(),
        format!("--text={prompt}"),
        if multiple {
            "--checklist"
        } else {
            "--radiolist"
        }
        .into(),
        "--column=".into(),
        "--column=#".into(),
        "--column=Name".into(),
        "--hide-column=2".into(),
        "--print-column=2".into(),
        "--separator=\n".into(),
        "--width=520".into(),
        "--height=360".into(),
    ];
    for (i, item) in items.iter().enumerate() {
        let on = preselect_all || i == 0;
        a.push(if on { "TRUE" } else { "FALSE" }.into());
        a.push((i + 1).to_string());
        a.push(item.replace('\n', " "));
    }
    a
}

/// `kdialog --inputbox` (or `--password`): prints the text.
pub fn kdialog_text_args(message: &str, default: &str, hidden: bool) -> Vec<String> {
    let mut a = vec!["--title".to_string(), "Itervox".into()];
    if hidden {
        a.extend(["--password".into(), message.into()]);
    } else {
        a.extend(["--inputbox".into(), message.into(), default.into()]);
    }
    a
}

/// `kdialog --checklist` (or `--radiolist`): prints the chosen tags (the
/// 1-based numbers), one per line.
pub fn kdialog_choice_args(
    prompt: &str,
    items: &[String],
    multiple: bool,
    preselect_all: bool,
) -> Vec<String> {
    let mut a = vec![
        "--title".to_string(),
        "Itervox".into(),
        "--separate-output".into(),
        if multiple {
            "--checklist"
        } else {
            "--radiolist"
        }
        .into(),
        prompt.into(),
    ];
    for (i, item) in items.iter().enumerate() {
        let on = preselect_all || i == 0;
        a.push((i + 1).to_string());
        a.push(item.replace('\n', " "));
        a.push(if on { "on" } else { "off" }.into());
    }
    a
}

/// A PowerShell single-quoted string literal.
pub fn ps_string(s: &str) -> String {
    format!("'{}'", s.replace('\'', "''"))
}

const PS_PREAMBLE: &str = "$ErrorActionPreference='Stop'\n[Console]::OutputEncoding=[Text.Encoding]::UTF8\nAdd-Type -AssemblyName System.Windows.Forms\n[Windows.Forms.Application]::EnableVisualStyles()\n$f=New-Object Windows.Forms.Form\n$f.Text='Itervox';$f.StartPosition='CenterScreen';$f.TopMost=$true;$f.FormBorderStyle='FixedDialog';$f.MaximizeBox=$false;$f.MinimizeBox=$false\n";

const PS_BUTTONS: &str = "$ok=New-Object Windows.Forms.Button;$ok.Text='OK';$ok.DialogResult='OK'\n$no=New-Object Windows.Forms.Button;$no.Text='Cancel';$no.DialogResult='Cancel'\n$f.AcceptButton=$ok;$f.CancelButton=$no\n";

/// A Windows Forms dialog with a text field; prints the text, exits 1 on
/// Cancel.
pub fn powershell_text_script(message: &str, default: &str, hidden: bool) -> String {
    format!(
        "{PS_PREAMBLE}$f.ClientSize=New-Object Drawing.Size(460,130)\n$l=New-Object Windows.Forms.Label;$l.Text={};$l.SetBounds(12,10,436,44)\n$t=New-Object Windows.Forms.TextBox;$t.Text={};$t.SetBounds(12,58,436,24){}\n{PS_BUTTONS}$ok.SetBounds(292,94,75,26);$no.SetBounds(373,94,75,26)\n$f.Controls.AddRange(@($l,$t,$ok,$no))\nif($f.ShowDialog() -eq 'OK'){{[Console]::Out.Write($t.Text);exit 0}}else{{exit 1}}\n",
        ps_string(message),
        ps_string(default),
        if hidden {
            ";$t.UseSystemPasswordChar=$true"
        } else {
            ""
        }
    )
}

/// A Windows Forms list (checked for `multiple`); prints the chosen items'
/// 1-based numbers, one per line, exits 1 on Cancel.
pub fn powershell_choice_script(
    prompt: &str,
    items: &[String],
    multiple: bool,
    preselect_all: bool,
) -> String {
    let list: Vec<String> = items
        .iter()
        .map(|i| ps_string(&i.replace('\n', " ")))
        .collect();
    let (make, select, read) = if multiple {
        (
            "$b=New-Object Windows.Forms.CheckedListBox;$b.CheckOnClick=$true",
            if preselect_all {
                "for($i=0;$i -lt $b.Items.Count;$i++){$b.SetItemChecked($i,$true)}"
            } else {
                "if($b.Items.Count -gt 0){$b.SetItemChecked(0,$true)}"
            },
            "$b.CheckedIndices | ForEach-Object { [Console]::Out.WriteLine($_+1) }",
        )
    } else {
        (
            "$b=New-Object Windows.Forms.ListBox",
            "if($b.Items.Count -gt 0){$b.SelectedIndex=0}",
            "if($b.SelectedIndex -ge 0){[Console]::Out.WriteLine($b.SelectedIndex+1)}",
        )
    };
    format!(
        "{PS_PREAMBLE}$f.ClientSize=New-Object Drawing.Size(480,330)\n$l=New-Object Windows.Forms.Label;$l.Text={};$l.SetBounds(12,10,456,36)\n{make};$b.SetBounds(12,50,456,232)\n$b.Items.AddRange(@({}))\n{select}\n{PS_BUTTONS}$ok.SetBounds(312,292,75,26);$no.SetBounds(393,292,75,26)\n$f.Controls.AddRange(@($l,$b,$ok,$no))\nif($f.ShowDialog() -eq 'OK'){{{read};exit 0}}else{{exit 1}}\n",
        ps_string(prompt),
        list.join(",")
    )
}

/// PowerShell's arguments for `script`: no profile, no prompt, the script
/// passed as UTF-16LE base64 (`-EncodedCommand`), so no quoting of the
/// command line is involved.
pub fn powershell_args(script: &str) -> Vec<String> {
    let utf16: Vec<u8> = script.encode_utf16().flat_map(u16::to_le_bytes).collect();
    vec![
        "-NoProfile".into(),
        "-NonInteractive".into(),
        "-ExecutionPolicy".into(),
        "Bypass".into(),
        "-STA".into(),
        "-EncodedCommand".into(),
        base64(&utf16),
    ]
}

/// Standard base64 with padding.
pub fn base64(data: &[u8]) -> String {
    const T: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::with_capacity(data.len().div_ceil(3) * 4);
    for chunk in data.chunks(3) {
        let b = [
            chunk[0],
            chunk.get(1).copied().unwrap_or(0),
            chunk.get(2).copied().unwrap_or(0),
        ];
        let n = (u32::from(b[0]) << 16) | (u32::from(b[1]) << 8) | u32::from(b[2]);
        for (i, shift) in [18, 12, 6, 0].into_iter().enumerate() {
            if i <= chunk.len() {
                out.push(T[((n >> shift) & 63) as usize] as char);
            } else {
                out.push('=');
            }
        }
    }
    out
}

/// The 0-based indices in a helper's output: 1-based numbers, one per line.
pub fn parse_indices(out: &str, n: usize) -> Vec<usize> {
    let mut v: Vec<usize> = out
        .lines()
        .filter_map(|l| l.trim().parse::<usize>().ok())
        .filter(|i| (1..=n).contains(i))
        .map(|i| i - 1)
        .collect();
    v.sort_unstable();
    v.dedup();
    v
}

/// Runs a dialog helper: its stdout on exit 0, `None` on exit 1 (Cancel),
/// the last line of stderr otherwise.
pub fn run_helper(program: &Path, args: &[String]) -> Result<Option<String>, String> {
    let mut c = Command::new(program);
    c.args(args).stdin(Stdio::null());
    crate::platform::no_console(&mut c);
    let out = c
        .output()
        .map_err(|e| format!("could not show a dialog ({}): {e}", program.display()))?;
    if out.status.code() == Some(1) {
        return Ok(None);
    }
    if !out.status.success() {
        let stderr = String::from_utf8_lossy(&out.stderr);
        return Err(format!(
            "{} failed ({}): {}",
            program.display(),
            out.status,
            crate::supervisor::last_line(&stderr)
        ));
    }
    let s = String::from_utf8_lossy(&out.stdout);
    let s = s.strip_suffix('\n').unwrap_or(&s);
    Ok(Some(s.strip_suffix('\r').unwrap_or(s).to_string()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn strings_are_escaped() {
        assert_eq!(applescript_string(r#"a "b" \c"#), r#""a \"b\" \\c""#);
        assert_eq!(applescript_string("x\ny"), r#""x\ny""#);
    }

    #[test]
    fn token_prompt_hides_the_answer() {
        let s = text_prompt_script("Token for app", "", true);
        assert!(s.contains("with hidden answer"));
        assert!(s.contains(r#"display dialog "Token for app" default answer """#));
        assert!(!text_prompt_script("Host", "me@box", false).contains("hidden"));
    }

    #[test]
    fn choices_are_numbered_and_parsed_back() {
        let items = vec!["app, eu".to_string(), "api".to_string()];
        let s = choose_script("Daemons", &items, true, true);
        assert!(s.contains(r#"{"1. app, eu", "2. api"}"#), "{s}");
        assert!(s.contains("with multiple selections allowed"));
        assert!(s.contains("default items {\"1. app, eu\", \"2. api\"}"));
        assert_eq!(parse_choice("1. app, eu\n2. api", 2), vec![0, 1]);
        assert_eq!(parse_choice("2. api", 2), vec![1]);
        assert_eq!(parse_choice("", 2), Vec::<usize>::new());
        assert_eq!(parse_choice("9. nope", 2), Vec::<usize>::new());
    }

    #[test]
    fn cancel_is_recognised() {
        assert!(is_cancel("execution error: User canceled. (-128)"));
        assert!(!is_cancel("execution error: boom (-1700)"));
    }

    #[test]
    fn linux_says_when_neither_dialog_helper_exists() {
        let dir = tempfile::tempdir().unwrap();
        let empty = dir.path().join("empty");
        std::fs::create_dir_all(&empty).unwrap();
        let err = find_helper(Os::Linux, empty.as_os_str(), "GNOME").unwrap_err();
        assert!(err.contains("zenity") && err.contains("kdialog"), "{err}");
        assert_eq!(
            find_helper(Os::Windows, empty.as_os_str(), ""),
            Ok(Helper::PowerShell(PathBuf::from("powershell.exe")))
        );
    }

    #[cfg(unix)]
    #[test]
    fn linux_dialogs_prefer_kdialog_on_kde_and_zenity_elsewhere() {
        use std::os::unix::fs::PermissionsExt;
        let dir = tempfile::tempdir().unwrap();
        for n in ["zenity", "kdialog"] {
            let p = dir.path().join(n);
            std::fs::write(&p, "#!/bin/sh\n").unwrap();
            std::fs::set_permissions(&p, std::fs::Permissions::from_mode(0o755)).unwrap();
        }
        let path = dir.path().as_os_str();
        assert_eq!(
            find_helper(Os::Linux, path, "ubuntu:GNOME"),
            Ok(Helper::Zenity(dir.path().join("zenity")))
        );
        assert_eq!(
            find_helper(Os::Linux, path, "KDE"),
            Ok(Helper::Kdialog(dir.path().join("kdialog")))
        );
    }

    #[test]
    fn zenity_and_kdialog_arguments() {
        let t = zenity_text_args("Token for app", "", true);
        assert!(t.contains(&"--entry".to_string()));
        assert!(t.contains(&"--text=Token for app".to_string()));
        assert!(t.contains(&"--hide-text".to_string()));
        assert!(!zenity_text_args("Host", "me@box", false).contains(&"--hide-text".to_string()));
        let items = vec!["app, eu".to_string(), "api".to_string()];
        let c = zenity_choice_args("Daemons", &items, true, false);
        assert!(c.contains(&"--checklist".to_string()));
        assert_eq!(
            &c[c.len() - 6..],
            ["TRUE", "1", "app, eu", "FALSE", "2", "api"]
        );
        assert!(zenity_choice_args("x", &items, false, false).contains(&"--radiolist".to_string()));
        assert_eq!(
            kdialog_text_args("Token", "", true),
            ["--title", "Itervox", "--password", "Token"]
        );
        assert_eq!(
            kdialog_text_args("Host", "me@box", false)[2..],
            ["--inputbox", "Host", "me@box"]
        );
        let k = kdialog_choice_args("Daemons", &items, true, true);
        assert_eq!(
            &k[..5],
            [
                "--title",
                "Itervox",
                "--separate-output",
                "--checklist",
                "Daemons"
            ]
        );
        assert_eq!(&k[5..], ["1", "app, eu", "on", "2", "api", "on"]);
        assert_eq!(parse_indices("1\n2\n", 2), vec![0, 1]);
        assert_eq!(parse_indices("2\r\n9\nx", 2), vec![1]);
    }

    #[test]
    fn windows_dialogs_are_encoded_powershell_forms() {
        assert_eq!(ps_string("it's"), "'it''s'");
        let s = powershell_text_script("Token for 'app'", "", true);
        assert!(s.contains("$l.Text='Token for ''app'''"), "{s}");
        assert!(s.contains("UseSystemPasswordChar=$true"));
        assert!(!powershell_text_script("Host", "x", false).contains("UseSystemPasswordChar"));
        let items = vec!["a".to_string(), "b".to_string()];
        let c = powershell_choice_script("Daemons", &items, true, true);
        assert!(
            c.contains("CheckedListBox") && c.contains("$b.Items.AddRange(@('a','b'))"),
            "{c}"
        );
        assert!(powershell_choice_script("D", &items, false, false).contains("SelectedIndex=0"));
        let args = powershell_args("x");
        assert_eq!(args[args.len() - 2], "-EncodedCommand");
        // "x" in UTF-16LE is 78 00.
        assert_eq!(args[args.len() - 1], "eAA=");
        assert_eq!(base64(b"Man"), "TWFu");
        assert_eq!(base64(b"Ma"), "TWE=");
        assert_eq!(base64(b""), "");
    }

    /// The whole Linux path against a stand-in zenity: OK prints the
    /// answer, Cancel (exit 1) is `None`, another failure is an error.
    #[cfg(unix)]
    #[test]
    fn a_linux_helper_answers_cancels_and_fails() {
        use std::os::unix::fs::PermissionsExt;
        let dir = tempfile::tempdir().unwrap();
        let z = dir.path().join("zenity");
        let mk = |body: &str| {
            std::fs::write(&z, format!("#!/bin/sh\n{body}\n")).unwrap();
            std::fs::set_permissions(&z, std::fs::Permissions::from_mode(0o755)).unwrap();
        };
        mk(
            r#"for a in "$@"; do case "$a" in --entry-text=*) echo "${a#--entry-text=}";; esac; done"#,
        );
        assert_eq!(
            run_helper(&z, &zenity_text_args("Host", "me@box", false)).unwrap(),
            Some("me@box".into())
        );
        mk("exit 1");
        assert_eq!(run_helper(&z, &[]).unwrap(), None);
        mk("echo 'cannot open display' >&2; exit 255");
        let err = run_helper(&z, &[]).unwrap_err();
        assert!(err.contains("cannot open display"), "{err}");
    }
}
