//! "Needs you": every issue blocked on a person, across all the projects the
//! app knows — local daemons read directly, members read through the hub
//! (`hub_source`: each member's summary carries its oldest items) and remote
//! daemons through their tunnels — oldest first, with how long each has
//! waited.
//!
//! The tray shows it as a "Needs you (N)" submenu (`menu.rs`); choosing an
//! item opens that project's dashboard at the issue (`?issue=`), where the
//! existing reply / approve paths answer it. Nothing here writes to a
//! daemon, and new items are announced by the existing notifications (the
//! per-daemon alerts, or the hub's when it notifies for its members): this
//! module adds no notifier.

use crate::daemon_state::{
    NeedsYou, NEEDS_YOU_APPROVAL, NEEDS_YOU_PIPELINE_CHANGE, NEEDS_YOU_QUESTION,
};
use crate::events::{issue_deep_link, rfc3339_unix};
use crate::model::{Health, ProjectView};
use crate::remote_model::ConnectionView;

/// One item of the app's "Needs you" view.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct NeedsYouItem {
    /// The project key the dashboard opens for (a WORKFLOW.md path, or a
    /// remote daemon's token key).
    pub project: String,
    pub project_name: String,
    pub identifier: String,
    pub title: String,
    pub kind: String,
    pub summary: String,
    /// Seconds since the Unix epoch; `None` when the daemon gave no time.
    pub since: Option<i64>,
    /// The dashboard deep link that opens the issue; empty when the
    /// daemon's dashboard address is unknown (the item is shown disabled).
    pub url: String,
}

fn item(project: &str, name: &str, base_url: Option<&str>, n: &NeedsYou) -> NeedsYouItem {
    NeedsYouItem {
        project: project.to_string(),
        project_name: name.to_string(),
        identifier: n.identifier.clone(),
        title: n.title.clone(),
        kind: n.kind.clone(),
        summary: n.summary.clone(),
        since: rfc3339_unix(&n.waiting_since),
        url: base_url
            .filter(|b| !b.is_empty())
            .map(|b| issue_deep_link(b, &n.identifier))
            .unwrap_or_default(),
    }
}

/// Every item of every project whose state was read, oldest first (items
/// without a time last, then by project and identifier).
pub fn collect(views: &[ProjectView], remotes: &[ConnectionView]) -> Vec<NeedsYouItem> {
    let mut out = Vec::new();
    for v in views {
        if let Health::Ok(st) = &v.health {
            let base = v.record.as_ref().map(|r| r.base_url());
            for n in &st.needs_you {
                out.push(item(&v.key, &v.name, base.as_deref(), n));
            }
        }
    }
    for c in remotes {
        for d in &c.daemons {
            if let Health::Ok(st) = &d.health {
                let base = d.url();
                let name = format!("{} ({})", d.name, c.name);
                for n in &st.needs_you {
                    out.push(item(&d.key, &name, base.as_deref(), n));
                }
            }
        }
    }
    sort_oldest_first(&mut out);
    out
}

/// Oldest first; unknown times last; ties by project, then identifier.
pub fn sort_oldest_first(items: &mut [NeedsYouItem]) {
    items.sort_by(|a, b| {
        let key = |i: &NeedsYouItem| (i.since.is_none(), i.since.unwrap_or(0));
        key(a)
            .cmp(&key(b))
            .then_with(|| a.project_name.cmp(&b.project_name))
            .then_with(|| a.identifier.cmp(&b.identifier))
    });
}

/// A wait as the app shows it: "under 1m", "14m", "2h 14m", "3d 4h".
pub fn wait_label(secs: i64) -> String {
    let secs = secs.max(0);
    let (m, h, d) = (secs / 60, secs / 3600, secs / 86_400);
    if m == 0 {
        "under 1m".into()
    } else if h == 0 {
        format!("{m}m")
    } else if d == 0 {
        format!("{h}h {}m", m % 60)
    } else {
        format!("{d}d {}h", h % 24)
    }
}

/// How a kind reads in the menu.
pub fn kind_label(kind: &str) -> &'static str {
    match kind {
        NEEDS_YOU_APPROVAL => "approval",
        NEEDS_YOU_PIPELINE_CHANGE => "pipeline change",
        NEEDS_YOU_QUESTION => "question",
        _ => "waiting",
    }
}

/// Longest summary in a menu label, in characters.
const MAX_SUMMARY_CHARS: usize = 70;

fn clip(s: &str, n: usize) -> String {
    if s.chars().count() <= n {
        s.to_string()
    } else {
        let mut out: String = s.chars().take(n - 1).collect();
        out.push('…');
        out
    }
}

/// The menu label of `it` at `now` (Unix seconds), e.g.
/// "2h 14m · ENG-12 · question: Which database? — web-app".
pub fn menu_label(it: &NeedsYouItem, now: i64) -> String {
    let wait = it
        .since
        .map(|s| wait_label(now - s))
        .unwrap_or_else(|| "—".into());
    let mut label = format!("{wait} · {} · {}", it.identifier, kind_label(&it.kind));
    if !it.summary.is_empty() {
        label.push_str(": ");
        label.push_str(&clip(&it.summary, MAX_SUMMARY_CHARS));
    }
    label.push_str(" — ");
    label.push_str(&it.project_name);
    label
}

/// The submenu's title: "Needs you (3)".
pub fn title(count: usize) -> String {
    format!("Needs you ({count})")
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::daemon_state::StateSummary;
    use crate::model::Health;
    use crate::status::DaemonRecord;

    fn ny(kind: &str, id: &str, since: &str) -> NeedsYou {
        NeedsYou {
            kind: kind.into(),
            identifier: id.into(),
            title: String::new(),
            summary: format!("about {id}"),
            waiting_since: since.into(),
        }
    }

    fn view(key: &str, name: &str, url: &str, needs: Vec<NeedsYou>) -> ProjectView {
        ProjectView {
            key: key.into(),
            name: name.into(),
            record: Some(DaemonRecord {
                url: url.into(),
                ..Default::default()
            }),
            added: true,
            managed: false,
            health: Health::Ok(StateSummary {
                needs_you: needs,
                ..Default::default()
            }),
            pending: None,
            error: None,
        }
    }

    #[test]
    fn collects_every_project_oldest_first_with_issue_links() {
        let views = vec![
            view(
                "/a/WORKFLOW.md",
                "alpha",
                "http://127.0.0.1:8090/",
                vec![
                    ny(NEEDS_YOU_QUESTION, "ENG-2", "2026-10-14T10:00:00Z"),
                    ny(NEEDS_YOU_APPROVAL, "ENG-9", ""),
                ],
            ),
            view(
                "/b/WORKFLOW.md",
                "beta",
                "http://127.0.0.1:8091/",
                vec![ny(NEEDS_YOU_PIPELINE_CHANGE, "#12", "2026-10-14T09:00:00Z")],
            ),
            ProjectView {
                health: Health::Stopped,
                ..view("/c/WORKFLOW.md", "gamma", "", vec![])
            },
        ];
        let items = collect(&views, &[]);
        let got: Vec<_> = items
            .iter()
            .map(|i| (i.project_name.as_str(), i.identifier.as_str()))
            .collect();
        assert_eq!(
            got,
            vec![("beta", "#12"), ("alpha", "ENG-2"), ("alpha", "ENG-9")],
            "oldest first across projects, unknown time last"
        );
        assert_eq!(items[0].url, "http://127.0.0.1:8091/?issue=%2312");
        assert_eq!(items[0].project, "/b/WORKFLOW.md");
        assert_eq!(items[2].since, None);
    }

    #[test]
    fn labels_the_wait_kind_and_project() {
        assert_eq!(wait_label(30), "under 1m");
        assert_eq!(wait_label(14 * 60 + 5), "14m");
        assert_eq!(wait_label((2 * 60 + 14) * 60), "2h 14m");
        assert_eq!(wait_label((3 * 24 + 4) * 3600), "3d 4h");
        assert_eq!(
            wait_label(-5),
            "under 1m",
            "clock skew is not a negative wait"
        );

        let since = rfc3339_unix("2026-10-14T09:46:00Z").unwrap();
        let it = NeedsYouItem {
            project: "/a/WORKFLOW.md".into(),
            project_name: "web-app".into(),
            identifier: "ENG-12".into(),
            title: String::new(),
            kind: NEEDS_YOU_QUESTION.into(),
            summary: "Which database?".into(),
            since: Some(since),
            url: String::new(),
        };
        assert_eq!(
            menu_label(&it, since + (2 * 60 + 14) * 60),
            "2h 14m · ENG-12 · question: Which database? — web-app"
        );
        let pipeline = NeedsYouItem {
            kind: NEEDS_YOU_PIPELINE_CHANGE.into(),
            summary: "x".repeat(100),
            since: None,
            ..it
        };
        let label = menu_label(&pipeline, 0);
        assert!(
            label.starts_with("— · ENG-12 · pipeline change: xxx"),
            "{label}"
        );
        assert!(label.contains("x…"), "{label}");
        assert_eq!(title(3), "Needs you (3)");
    }

    /// The tray: a "Needs you (N)" submenu under the header, its items
    /// opening the project's dashboard at the issue; a count larger than
    /// the listed items (a hub member older than the lane) is said; nothing
    /// when nothing waits.
    #[test]
    fn the_tray_lists_needs_you_under_the_header() {
        use crate::menu::{append_needs_you_section, build_menu, MenuAction, MenuEntry};
        let views = vec![view(
            "/a/WORKFLOW.md",
            "alpha",
            "http://127.0.0.1:8090/",
            vec![ny(NEEDS_YOU_QUESTION, "ENG-2", "2026-10-14T10:00:00Z")],
        )];
        let items = collect(&views, &[]);
        let now = rfc3339_unix("2026-10-14T12:14:00Z").unwrap();
        let mut spec = build_menu(&views, &[], std::iter::empty(), None);
        let before = spec.entries.len();
        append_needs_you_section(&mut spec, &items, 3, now);
        assert_eq!(spec.entries.len(), before + 2);
        assert!(matches!(spec.entries[1], MenuEntry::Separator));
        let MenuEntry::Submenu { label, items: sub } = &spec.entries[2] else {
            panic!("no submenu: {:?}", spec.entries[2]);
        };
        assert_eq!(label, "Needs you (3)");
        let MenuEntry::Item {
            id, label, enabled, ..
        } = &sub[0]
        else {
            panic!("no item");
        };
        assert_eq!(label, "2h 14m · ENG-2 · question: about ENG-2 — alpha");
        assert!(enabled);
        assert_eq!(
            spec.actions.get(id),
            Some(&MenuAction::OpenUrl {
                project: "/a/WORKFLOW.md".into(),
                url: "http://127.0.0.1:8090/?issue=ENG-2".into(),
            })
        );
        let MenuEntry::Item { label, enabled, .. } = &sub[1] else {
            panic!("no count line");
        };
        assert_eq!(label, "…and 2 more (see each project's dashboard)");
        assert!(!enabled);

        let mut empty = build_menu(&[], &[], std::iter::empty(), None);
        let n = empty.entries.len();
        append_needs_you_section(&mut empty, &[], 0, now);
        assert_eq!(empty.entries.len(), n, "nothing waits: no submenu");
    }
}
