//! How "Open in Terminal" starts each terminal program
//! ([`crate::terminal_app`]) running the attach command (#278).
//!
//! Always an argument vector, never a shell line, with the attach command's
//! words checked first ([`check_argv`]: none empty, none with a control
//! character, the first not an option):
//!
//! - **macOS** opens apps through LaunchServices (`/usr/bin/open`), so the
//!   attach command goes in the one-shot `.command` file
//!   ([`crate::terminal::command_file`]: mode 0700, created new, deleting
//!   itself, each word shell-quoted), and the terminal is handed only that
//!   file's path: Terminal, iTerm2 and Warp open it as a document
//!   (`open -a <App> <file>`, as Terminal always has; no AppleScript), and
//!   Ghostty, WezTerm, kitty and Alacritty, which open no documents, start
//!   a new instance that runs it (`open -na <App> --args -e <file>`,
//!   `--args start -- <file>`, `--args <file>`).
//! - **Linux** runs the program with its own "run this command" flag and
//!   the words after it.
//! - **Windows**: Windows Terminal takes the words after `new-tab` (it
//!   splits its command line at `;`, so no word may hold one). PowerShell
//!   and Command Prompt only take a command line: the app builds it from
//!   literals — `& 'word' 'word' …` for PowerShell (a single-quoted string
//!   expands nothing; a word with a quote character is refused), and for
//!   Command Prompt the words, those with spaces in double quotes, between
//!   the outer quotes `/S /K` strips (a word with `" % ^ & | < > !` is
//!   refused) — and starts it in a new console.

use std::path::Path;

use crate::platform::Os;
use crate::terminal::{command_file, Launch};
use crate::terminal_app::{App, Detected};

/// The attach command's words are usable: at least one, the first not an
/// option, none empty or holding a control character.
pub fn check_argv(argv: &[String]) -> Result<(), String> {
    if argv.is_empty() || argv[0].starts_with('-') {
        return Err("Nothing to run in the terminal.".into());
    }
    if argv
        .iter()
        .any(|w| w.is_empty() || w.chars().any(|c| c.is_control()))
    {
        return Err("The terminal command holds an unusable argument.".into());
    }
    Ok(())
}

fn unusable() -> String {
    "The terminal command holds an unusable argument.".into()
}

/// How `os` starts `app` running `argv`. On macOS `script` is where the
/// `.command` file goes (the caller writes it first).
pub fn launch_in(
    os: Os,
    app: &Detected,
    argv: &[String],
    script: Option<&Path>,
) -> Result<Launch, String> {
    check_argv(argv)?;
    let program = app.program.to_string_lossy().into_owned();
    if program.is_empty() || program.starts_with('-') || program.chars().any(|c| c.is_control()) {
        return Err(format!("{} cannot be started from here.", app.name()));
    }
    let not_here = || {
        Err(format!(
            "{} is not a terminal on this platform.",
            app.name()
        ))
    };
    let plain = |args: Vec<String>| Launch {
        program: program.clone(),
        args,
        script: None,
        new_console: false,
        raw_tail: None,
    };
    let with = |flags: &[&str]| {
        let mut a: Vec<String> = flags.iter().map(|s| s.to_string()).collect();
        a.extend(argv.iter().cloned());
        a
    };
    match os {
        Os::Mac => {
            let path = script.ok_or("No place to write the terminal command file.")?;
            let file = path.to_string_lossy().into_owned();
            let args: Vec<String> = match app.app {
                App::MacTerminal | App::ITerm2 | App::Warp => vec!["-a".into(), program, file],
                App::Ghostty | App::Alacritty => {
                    vec!["-na".into(), program, "--args".into(), "-e".into(), file]
                }
                App::WezTerm => vec![
                    "-na".into(),
                    program,
                    "--args".into(),
                    "start".into(),
                    "--".into(),
                    file,
                ],
                App::Kitty => vec!["-na".into(), program, "--args".into(), file],
                _ => return not_here(),
            };
            Ok(Launch {
                program: "/usr/bin/open".into(),
                args,
                script: Some((path.to_path_buf(), command_file(argv))),
                new_console: false,
                raw_tail: None,
            })
        }
        Os::Linux => Ok(plain(match app.app {
            App::GnomeTerminal => with(&["--"]),
            App::WezTerm => with(&["start", "--"]),
            App::Kitty | App::Foot => with(&[]),
            App::Xfce4Terminal => with(&["-x"]),
            App::Konsole
            | App::Alacritty
            | App::Ghostty
            | App::Tilix
            | App::Xterm
            | App::XTerminalEmulator
            | App::Configured => with(&["-e"]),
            _ => return not_here(),
        })),
        Os::Windows => match app.app {
            App::WindowsTerminal => {
                if argv.iter().any(|w| w.contains(';')) {
                    return Err(unusable());
                }
                Ok(plain(with(&["new-tab"])))
            }
            App::Pwsh | App::WindowsPowerShell => Ok(Launch {
                new_console: true,
                ..plain(vec![
                    "-NoExit".into(),
                    "-Command".into(),
                    powershell_line(argv)?,
                ])
            }),
            App::Cmd => Ok(Launch {
                new_console: true,
                raw_tail: Some(cmd_line(argv)?),
                ..plain(vec!["/S".into(), "/K".into()])
            }),
            _ => not_here(),
        },
    }
}

/// `& 'w0' 'w1' …`: PowerShell's call operator on single-quoted literals,
/// which expand nothing. A word holding a quote character (PowerShell also
/// ends a literal at the typographic single quotes) is refused.
pub fn powershell_line(argv: &[String]) -> Result<String, String> {
    let quotes = ['\'', '"', '\u{2018}', '\u{2019}', '\u{201A}', '\u{201B}'];
    if argv.iter().any(|w| w.contains(quotes)) {
        return Err(unusable());
    }
    let words: Vec<String> = argv.iter().map(|w| format!("'{w}'")).collect();
    Ok(format!("& {}", words.join(" ")))
}

/// The command line after `cmd.exe /S /K`: the words, each holding a space
/// or a parenthesis in double quotes, inside one more pair of quotes that
/// `/S` strips. Words with characters Command Prompt reads even inside
/// quotes (`"`, `%`, `^`, `!`) or outside them (`&`, `|`, `<`, `>`) are
/// refused.
pub fn cmd_line(argv: &[String]) -> Result<String, String> {
    if argv
        .iter()
        .any(|w| w.contains(['"', '%', '^', '!', '&', '|', '<', '>']))
    {
        return Err(unusable());
    }
    let words: Vec<String> = argv
        .iter()
        .map(|w| {
            if w.contains([' ', '\t', '(', ')']) {
                format!("\"{w}\"")
            } else {
                w.clone()
            }
        })
        .collect();
    Ok(format!("\"{}\"", words.join(" ")))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::PathBuf;

    fn v(words: &[&str]) -> Vec<String> {
        words.iter().map(|s| s.to_string()).collect()
    }

    fn attach() -> Vec<String> {
        v(&["tmux", "attach", "-t", "itervox-abc123"])
    }

    /// macOS: every app gets only the one-shot `.command` file; Terminal,
    /// iTerm2 and Warp open it, the others run it in a new instance.
    #[test]
    fn terminal_launch_mac_argv_per_program() {
        let script = PathBuf::from("/tmp/itervox-attach-1-2.command");
        let f = "/tmp/itervox-attach-1-2.command";
        let cases: [(App, &str, Vec<String>); 7] = [
            (
                App::MacTerminal,
                "/System/Applications/Utilities/Terminal.app",
                v(&["-a", "/System/Applications/Utilities/Terminal.app", f]),
            ),
            (
                App::ITerm2,
                "/Applications/iTerm.app",
                v(&["-a", "/Applications/iTerm.app", f]),
            ),
            (
                App::Warp,
                "/Applications/Warp.app",
                v(&["-a", "/Applications/Warp.app", f]),
            ),
            (
                App::Ghostty,
                "/Applications/Ghostty.app",
                v(&["-na", "/Applications/Ghostty.app", "--args", "-e", f]),
            ),
            (
                App::WezTerm,
                "/Applications/WezTerm.app",
                v(&[
                    "-na",
                    "/Applications/WezTerm.app",
                    "--args",
                    "start",
                    "--",
                    f,
                ]),
            ),
            (
                App::Kitty,
                "/Users/me/Applications/kitty.app",
                v(&["-na", "/Users/me/Applications/kitty.app", "--args", f]),
            ),
            (
                App::Alacritty,
                "/Applications/Alacritty.app",
                v(&["-na", "/Applications/Alacritty.app", "--args", "-e", f]),
            ),
        ];
        for (app, bundle, want) in cases {
            let l = launch_in(
                Os::Mac,
                &Detected::new(app, bundle),
                &attach(),
                Some(&script),
            )
            .unwrap();
            assert_eq!(l.program, "/usr/bin/open", "{app:?}");
            assert_eq!(l.args, want, "{app:?}");
            let (path, body) = l.script.unwrap();
            assert_eq!(path, script);
            assert!(body.ends_with("exec tmux attach -t itervox-abc123\n"));
            assert!(!l.new_console && l.raw_tail.is_none());
            // The attach words reach the app only through the file.
            assert!(!l.args.iter().any(|a| a == "tmux"), "{app:?}");
        }
        let iterm = Detected::new(App::ITerm2, "/Applications/iTerm.app");
        assert!(launch_in(Os::Mac, &iterm, &attach(), None).is_err());
        for not_mac in [App::GnomeTerminal, App::Pwsh, App::Configured] {
            assert!(launch_in(
                Os::Mac,
                &Detected::new(not_mac, "x"),
                &attach(),
                Some(&script)
            )
            .is_err());
        }
    }

    /// Linux: each program's own "run this command" flag, then the words.
    #[test]
    fn terminal_launch_linux_argv_per_program() {
        let tail = attach();
        let cases: [(App, &[&str]); 12] = [
            (App::GnomeTerminal, &["--"]),
            (App::Konsole, &["-e"]),
            (App::Kitty, &[]),
            (App::Alacritty, &["-e"]),
            (App::WezTerm, &["start", "--"]),
            (App::Ghostty, &["-e"]),
            (App::Foot, &[]),
            (App::Xfce4Terminal, &["-x"]),
            (App::Tilix, &["-e"]),
            (App::Xterm, &["-e"]),
            (App::XTerminalEmulator, &["-e"]),
            (App::Configured, &["-e"]),
        ];
        for (app, flags) in cases {
            let l = launch_in(Os::Linux, &Detected::new(app, "/usr/bin/t"), &tail, None).unwrap();
            assert_eq!(l.program, "/usr/bin/t", "{app:?}");
            let mut want = v(flags);
            want.extend(tail.iter().cloned());
            assert_eq!(l.args, want, "{app:?}");
            assert_eq!(l.script, None);
            assert!(!l.new_console && l.raw_tail.is_none());
        }
        for not_linux in [App::MacTerminal, App::ITerm2, App::Warp, App::Cmd] {
            assert!(launch_in(Os::Linux, &Detected::new(not_linux, "x"), &tail, None).is_err());
        }
    }

    /// Windows: Windows Terminal takes the words; PowerShell and Command
    /// Prompt a command line of literals, in a new console.
    #[test]
    fn terminal_launch_windows_argv_per_program() {
        let ssh = v(&[
            r"C:\Windows\System32\OpenSSH\ssh.exe",
            "-t",
            "--",
            "me@vm-1",
            "tmux",
            "attach",
            "-t",
            "itervox-abc123",
        ]);
        let wt = launch_in(
            Os::Windows,
            &Detected::new(
                App::WindowsTerminal,
                r"C:\Users\me\AppData\Local\Microsoft\WindowsApps\wt.exe",
            ),
            &ssh,
            None,
        )
        .unwrap();
        assert_eq!(wt.args[0], "new-tab");
        assert_eq!(&wt.args[1..], &ssh[..]);
        assert!(!wt.new_console);

        for app in [App::Pwsh, App::WindowsPowerShell] {
            let l = launch_in(Os::Windows, &Detected::new(app, "pwsh.exe"), &ssh, None).unwrap();
            assert_eq!(
                l.args,
                v(&[
                    "-NoExit",
                    "-Command",
                    r"& 'C:\Windows\System32\OpenSSH\ssh.exe' '-t' '--' 'me@vm-1' 'tmux' 'attach' '-t' 'itervox-abc123'",
                ])
            );
            assert!(l.new_console);
        }

        let gcloud = v(&[
            r"C:\Program Files (x86)\Google\Cloud SDK\google-cloud-sdk\bin\gcloud.cmd",
            "compute",
            "ssh",
            "vm",
            "--command=tmux attach -t itervox-abc123",
        ]);
        let cmd = launch_in(
            Os::Windows,
            &Detected::new(App::Cmd, r"C:\Windows\System32\cmd.exe"),
            &gcloud,
            None,
        )
        .unwrap();
        assert_eq!(cmd.program, r"C:\Windows\System32\cmd.exe");
        assert_eq!(cmd.args, v(&["/S", "/K"]));
        assert_eq!(
            cmd.raw_tail.as_deref(),
            Some(
                r#"""C:\Program Files (x86)\Google\Cloud SDK\google-cloud-sdk\bin\gcloud.cmd" compute ssh vm "--command=tmux attach -t itervox-abc123"""#
            )
        );
        assert!(cmd.new_console);
        assert!(launch_in(Os::Windows, &Detected::new(App::ITerm2, "x"), &ssh, None).is_err());
    }

    /// The checks hold for every program: no option first, no control
    /// character, nothing a program's own command line would read.
    #[test]
    fn terminal_launch_refuses_unusable_words_for_every_program() {
        let script = PathBuf::from("/tmp/x.command");
        let bad = [
            v(&[]),
            v(&["-e", "x"]),
            v(&["--help"]),
            v(&["tmux", "a\u{7}"]),
            v(&["tmux", "a\nb"]),
            v(&["tmux", ""]),
        ];
        let every: Vec<(Os, App)> = [
            App::MacTerminal,
            App::ITerm2,
            App::Warp,
            App::Ghostty,
            App::WezTerm,
            App::Kitty,
            App::Alacritty,
        ]
        .into_iter()
        .map(|a| (Os::Mac, a))
        .chain(
            crate::terminal_app::LINUX_PATH_ORDER
                .into_iter()
                .chain([App::Configured])
                .map(|a| (Os::Linux, a)),
        )
        .chain(
            [
                App::WindowsTerminal,
                App::Pwsh,
                App::WindowsPowerShell,
                App::Cmd,
            ]
            .into_iter()
            .map(|a| (Os::Windows, a)),
        )
        .collect();
        for (os, app) in &every {
            let d = Detected::new(*app, "/usr/bin/t");
            for argv in &bad {
                assert!(
                    launch_in(*os, &d, argv, Some(&script)).is_err(),
                    "{os:?} {app:?} {argv:?}"
                );
            }
            for program in ["-x", "", "/usr/bin/t\n"] {
                assert!(
                    launch_in(*os, &Detected::new(*app, program), &attach(), Some(&script))
                        .is_err(),
                    "{app:?} {program:?}"
                );
            }
        }
        let win = |app| Detected::new(app, "x.exe");
        assert!(launch_in(
            Os::Windows,
            &win(App::WindowsTerminal),
            &v(&["ssh", "a;b"]),
            None
        )
        .is_err());
        for w in ["it's", "a\"b", "a\u{2019}b"] {
            assert!(
                launch_in(Os::Windows, &win(App::Pwsh), &v(&["ssh", w]), None).is_err(),
                "{w:?}"
            );
        }
        for w in ["%PATH%", "a&b", "a|b", "a>b", "a^b", "a!b", "a\"b"] {
            assert!(
                launch_in(Os::Windows, &win(App::Cmd), &v(&["ssh", w]), None).is_err(),
                "{w:?}"
            );
        }
        // The macOS file quotes anything unusual; the terminal sees a path.
        let l = launch_in(
            Os::Mac,
            &Detected::new(App::Ghostty, "/Applications/Ghostty.app"),
            &v(&["/Apps/My SSH/ssh", "x"]),
            Some(&script),
        )
        .unwrap();
        assert!(l.script.unwrap().1.contains("exec '/Apps/My SSH/ssh' x\n"));
    }
}
