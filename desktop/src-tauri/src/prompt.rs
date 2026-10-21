//! Native prompts for the remote flows (#156) through `osascript`: a text
//! field (optionally hidden, for tokens) and a list to choose from. The app
//! is a menu-bar shell without its own UI pages; AppleScript dialogs are the
//! native macOS way to ask a question from one. This module builds the
//! scripts and reads their answers; `app_remote` runs them.

use std::process::{Command, Stdio};

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

/// Asks for a line of text; `None` when cancelled.
pub fn ask_text(message: &str, default: &str, hidden: bool) -> Result<Option<String>, String> {
    Ok(run_script(&text_prompt_script(message, default, hidden))?.map(|s| s.trim().to_string()))
}

/// Asks to choose from `items`; `None` when cancelled or nothing chosen.
pub fn ask_choice(
    prompt: &str,
    items: &[String],
    multiple: bool,
    preselect_all: bool,
) -> Result<Option<Vec<usize>>, String> {
    let out = run_script(&choose_script(prompt, items, multiple, preselect_all))?;
    Ok(out
        .map(|o| parse_choice(&o, items.len()))
        .filter(|v| !v.is_empty()))
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
}
