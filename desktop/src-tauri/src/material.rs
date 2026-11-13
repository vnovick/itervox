//! The system material behind the app's own pages: the project rail
//! (`rail.html`) and the Sessions window (`sessions.html`).
//!
//! Both pages draw a CSS imitation of glass (`desktop/dist/theme.css`).
//! Where the platform has a real material, the app puts it behind the page
//! instead and tells the page so, and the page lets it show through:
//!
//! - **macOS 26 and later**: Liquid Glass (AppKit's `NSGlassEffectView`).
//! - **Older macOS** (11 and later): vibrancy (`NSVisualEffectView`), the
//!   `sidebar` material behind the rail and `hudWindow` behind Sessions.
//! - **Windows 11** (build 22000 and later): Mica.
//! - **Linux, Windows 10**: none; the CSS look stays.
//!
//! "Reduce transparency" and "Increase contrast" (macOS), and the
//! transparency-effects and high-contrast settings (Windows), turn the
//! material off: the page keeps its opaque fallback colours.
//!
//! This module is the decision and the page side, as plain data with unit
//! tests; `app_material.rs` probes the system and applies the material.
//! The window must be transparent for the material to show, so the
//! decision is made before the window is built; when applying it fails
//! afterwards, [`CLEAR_SCRIPT`] takes the page back to the CSS look.

use crate::platform::Os;

/// The material behind one of the app's pages.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Material {
    /// macOS 26+: `NSGlassEffectView`.
    LiquidGlass,
    /// macOS before 26: `NSVisualEffectView` with this material.
    Vibrancy(VibrancyMaterial),
    /// Windows 11: the Mica system backdrop.
    Mica,
}

/// The `NSVisualEffectView.Material` used for vibrancy.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum VibrancyMaterial {
    /// `.sidebar`: the rail is the window's sidebar.
    Sidebar,
    /// `.hudWindow`: the Sessions window is a floating utility window.
    HudWindow,
}

impl Material {
    /// The page's `data-material` value.
    pub fn attr(self) -> &'static str {
        match self {
            Material::LiquidGlass => "liquid-glass",
            Material::Vibrancy(_) => "vibrancy",
            Material::Mica => "mica",
        }
    }
}

/// Which page the material goes behind.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Surface {
    /// The project rail of a dashboard window.
    Rail,
    /// The Sessions window.
    Sessions,
}

/// What the app read from the system before building a window.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub struct SystemLook {
    /// macOS major version (26 for macOS 26), when on macOS.
    pub macos_major: Option<u32>,
    /// AppKit has `NSGlassEffectView` (looked up at run time).
    pub glass_view: bool,
    /// Windows build number (22000 is the first Windows 11), when on
    /// Windows.
    pub windows_build: Option<u32>,
    /// macOS "Reduce transparency", or Windows with transparency effects
    /// off.
    pub reduce_transparency: bool,
    /// macOS "Increase contrast", or a Windows high-contrast theme.
    pub increase_contrast: bool,
}

/// The first macOS with Liquid Glass.
pub const MACOS_LIQUID_GLASS: u32 = 26;
/// The first Windows 11 build (Mica).
pub const WINDOWS_11_BUILD: u32 = 22000;

/// The material for `surface` on `os`, or `None` for the CSS look.
pub fn choose(os: Os, look: &SystemLook, surface: Surface) -> Option<Material> {
    if look.reduce_transparency || look.increase_contrast {
        return None;
    }
    match os {
        Os::Mac => {
            let major = look.macos_major?;
            if major >= MACOS_LIQUID_GLASS && look.glass_view {
                Some(Material::LiquidGlass)
            } else {
                Some(Material::Vibrancy(match surface {
                    Surface::Rail => VibrancyMaterial::Sidebar,
                    Surface::Sessions => VibrancyMaterial::HudWindow,
                }))
            }
        }
        Os::Windows => look
            .windows_build
            .filter(|b| *b >= WINDOWS_11_BUILD)
            .map(|_| Material::Mica),
        Os::Linux => None,
    }
}

/// The page global the initialization script sets; the page's head copies
/// it to `<html data-material>` (the script also sets the attribute itself
/// when `<html>` already exists).
pub const MATERIAL_GLOBAL: &str = "__ITERVOX_MATERIAL__";

/// The initialization script of a rail or Sessions webview with `m`
/// behind it. It acts only on the app's own pages (never on a page a
/// navigation might reach).
pub fn init_script(m: Material) -> String {
    let v = m.attr();
    format!(
        "(function(){{var o=window.location.origin;\
if(o!==\"tauri://localhost\"&&o!==\"http://tauri.localhost\"&&o!==\"https://tauri.localhost\")return;\
window.{MATERIAL_GLOBAL}=\"{v}\";\
var d=document.documentElement;if(d)d.setAttribute(\"data-material\",\"{v}\");}})();"
    )
}

/// Evaluated in the page when the material could not be applied after
/// all: back to the CSS look.
pub const CLEAR_SCRIPT: &str =
    "window.__ITERVOX_MATERIAL__=null;document.documentElement.removeAttribute(\"data-material\");";

#[cfg(test)]
mod tests {
    use super::*;

    fn mac(major: u32, glass_view: bool) -> SystemLook {
        SystemLook {
            macos_major: Some(major),
            glass_view,
            ..SystemLook::default()
        }
    }

    fn win(build: u32) -> SystemLook {
        SystemLook {
            windows_build: Some(build),
            ..SystemLook::default()
        }
    }

    #[test]
    fn material_follows_os_version_and_accessibility() {
        use Material::*;
        use Surface::*;
        use VibrancyMaterial::*;
        let reduce = |mut l: SystemLook| {
            l.reduce_transparency = true;
            l
        };
        let contrast = |mut l: SystemLook| {
            l.increase_contrast = true;
            l
        };
        let none = SystemLook::default();
        let cases: Vec<(&str, Os, SystemLook, Surface, Option<Material>)> = vec![
            ("macOS 26", Os::Mac, mac(26, true), Rail, Some(LiquidGlass)),
            (
                "macOS 26 sessions",
                Os::Mac,
                mac(26, true),
                Sessions,
                Some(LiquidGlass),
            ),
            ("macOS 27", Os::Mac, mac(27, true), Rail, Some(LiquidGlass)),
            // No class (or a compatibility version): vibrancy.
            (
                "macOS 26 no class",
                Os::Mac,
                mac(26, false),
                Rail,
                Some(Vibrancy(Sidebar)),
            ),
            (
                "macOS 25 with class",
                Os::Mac,
                mac(25, true),
                Rail,
                Some(Vibrancy(Sidebar)),
            ),
            (
                "macOS 15 rail",
                Os::Mac,
                mac(15, false),
                Rail,
                Some(Vibrancy(Sidebar)),
            ),
            (
                "macOS 15 sessions",
                Os::Mac,
                mac(15, false),
                Sessions,
                Some(Vibrancy(HudWindow)),
            ),
            (
                "macOS 11",
                Os::Mac,
                mac(11, false),
                Sessions,
                Some(Vibrancy(HudWindow)),
            ),
            ("macOS unknown", Os::Mac, none, Rail, None),
            (
                "macOS reduce transparency",
                Os::Mac,
                reduce(mac(26, true)),
                Rail,
                None,
            ),
            (
                "macOS reduce, old",
                Os::Mac,
                reduce(mac(14, false)),
                Rail,
                None,
            ),
            (
                "macOS more contrast",
                Os::Mac,
                contrast(mac(15, false)),
                Sessions,
                None,
            ),
            ("Windows 11", Os::Windows, win(22000), Sessions, Some(Mica)),
            ("Windows 11 rail", Os::Windows, win(26100), Rail, Some(Mica)),
            ("Windows 10", Os::Windows, win(19045), Sessions, None),
            ("Windows unknown", Os::Windows, none, Sessions, None),
            (
                "Windows transparency off",
                Os::Windows,
                reduce(win(26100)),
                Sessions,
                None,
            ),
            (
                "Windows high contrast",
                Os::Windows,
                contrast(win(26100)),
                Rail,
                None,
            ),
            ("Linux", Os::Linux, none, Rail, None),
            // Linux ignores what another platform would read.
            ("Linux, any look", Os::Linux, mac(26, true), Sessions, None),
        ];
        for (name, os, look, surface, want) in cases {
            assert_eq!(choose(os, &look, surface), want, "{name}");
        }
    }

    #[test]
    fn init_script_marks_only_the_app_pages() {
        let s = init_script(Material::LiquidGlass);
        let guard = r#"if(o!=="tauri://localhost"&&o!=="http://tauri.localhost"&&o!=="https://tauri.localhost")return;"#;
        let global = r#"window.__ITERVOX_MATERIAL__="liquid-glass";"#;
        let attr = r#"d.setAttribute("data-material","liquid-glass")"#;
        let g = s.find(guard).expect("origin guard");
        let f = s.find(global).expect("global");
        let a = s.find(attr).expect("attribute");
        assert!(g < f && f < a, "{s}");
        let vib = init_script(Material::Vibrancy(VibrancyMaterial::Sidebar));
        assert!(vib.contains(r#"="vibrancy";"#), "{vib}");
        assert!(init_script(Material::Mica).contains(r#"="mica";"#));
        assert!(CLEAR_SCRIPT.contains(MATERIAL_GLOBAL));
        assert!(CLEAR_SCRIPT.contains(r#"removeAttribute("data-material")"#));
    }

    /// The two pages adopt the flag in their `<head>`, before the
    /// stylesheet and the first paint; the placeholder page never does.
    #[test]
    fn pages_adopt_the_material_flag() {
        let dist = concat!(env!("CARGO_MANIFEST_DIR"), "/../dist/");
        let adopt = r#"<script>(function(){var m=window.__ITERVOX_MATERIAL__;if(m)document.documentElement.setAttribute("data-material",m);})();</script>"#;
        let link = r#"<link rel="stylesheet" href="theme.css">"#;
        for page in ["rail.html", "sessions.html"] {
            let html = std::fs::read_to_string(format!("{dist}{page}")).expect(page);
            let head = html.split_once("</head>").expect(page).0;
            let (a, l) = (head.find(adopt), head.find(link));
            assert!(a.is_some(), "{page} adopts the material flag");
            assert!(a < l, "{page}: flag before the stylesheet");
        }
        let index = std::fs::read_to_string(format!("{dist}index.html")).unwrap();
        assert!(!index.contains(MATERIAL_GLOBAL));
    }

    /// The innermost rule blocks of a stylesheet, as (selector, body); an
    /// `@media` wrapper is not a rule.
    fn blocks(css: &str) -> Vec<(String, String)> {
        let mut clean = String::new();
        let mut rest = css;
        while let Some(i) = rest.find("/*") {
            clean.push_str(&rest[..i]);
            rest = rest[i..].split_once("*/").map(|(_, r)| r).unwrap_or("");
        }
        clean.push_str(rest);
        let mut out = Vec::new();
        let (mut last, mut body_start) = (0, 0);
        let mut sel: Option<String> = None;
        for (i, c) in clean.char_indices() {
            match c {
                '{' => {
                    sel = Some(clean[last..i].trim().to_string());
                    body_start = i + 1;
                    last = i + 1;
                }
                '}' => {
                    if let Some(s) = sel.take() {
                        out.push((s, clean[body_start..i].trim().to_string()));
                    }
                    last = i + 1;
                }
                _ => {}
            }
        }
        out
    }

    fn has_colour_literal(body: &str) -> bool {
        if ["rgb(", "rgba(", "hsl("].iter().any(|f| body.contains(f)) {
            return true;
        }
        body.match_indices('#').any(|(i, _)| {
            let hex: String = body[i + 1..]
                .chars()
                .take_while(|c| c.is_ascii_hexdigit())
                .collect();
            matches!(hex.len(), 3 | 4 | 6 | 8)
        })
    }

    /// theme.css: with a native material the page is transparent and the
    /// panels tinted (no CSS blur of their own); reduced transparency,
    /// more contrast and forced colours take it back to opaque; the
    /// material rules use tokens only, no colour literals.
    #[test]
    fn theme_lets_the_native_material_show_through() {
        let path = concat!(env!("CARGO_MANIFEST_DIR"), "/../dist/theme.css");
        let css = std::fs::read_to_string(path).expect("dist/theme.css");
        let all = blocks(&css);
        let rule = |sel: &str| -> Vec<String> {
            all.iter()
                .filter(|(s, _)| s.split(',').any(|p| p.trim() == sel))
                .map(|(_, b)| b.clone())
                .collect()
        };
        let material: Vec<_> = all
            .iter()
            .filter(|(s, _)| s.contains("data-material"))
            .collect();
        assert!(material.len() >= 9, "{material:?}");
        for (sel, body) in &material {
            assert!(!has_colour_literal(body), "{sel} has a colour literal");
        }
        // Transparent page, no colour field.
        let page = rule(":root[data-material] body");
        let transparent = "background: transparent;".to_string();
        assert!(page.contains(&transparent), "{page:?}");
        let field = rule(":root[data-material] .page::before");
        assert_eq!(field, vec!["display: none;".to_string()]);
        // Tinted panels without their own blur.
        let glass = rule(":root[data-material] .glass");
        assert!(
            glass
                .iter()
                .any(|b| b.contains("background: var(--native-tint);")
                    && b.contains("-webkit-backdrop-filter: none; backdrop-filter: none;")),
            "{glass:?}"
        );
        // The tint is a token, light and dark.
        assert_eq!(css.matches("--native-tint:").count(), 2);
        // The opaque fallbacks.
        for media in [
            "@media (prefers-reduced-transparency: reduce)",
            "@media (prefers-contrast: more)",
            "@media (forced-colors: active)",
        ] {
            assert_eq!(css.matches(media).count(), 1, "{media}");
            let block = css.split_once(media).unwrap().1;
            let block = &block[..block.find("\n}").expect(media)];
            let opaque_page =
                ":root[data-material], :root[data-material] body { background: var(--bg); }";
            assert!(block.contains(opaque_page), "{media}: opaque page");
            let opaque_glass = "background: var(--glass-solid); }";
            let has_glass = block
                .lines()
                .any(|l| l.contains(":root[data-material] .glass") && l.contains(opaque_glass));
            assert!(has_glass, "{media}: opaque panels");
        }
    }
}
