//! Tauri glue of the system material behind the rail and the Sessions
//! window (see [`crate::material`]): reads the system before a window is
//! built and applies the material once it is. Main thread only (AppKit).

use tauri::Window;

use crate::material::{choose, Material, Surface, SystemLook, CLEAR_SCRIPT};
use crate::platform::Os;

/// The material for `surface` on this system, or `None` for the CSS look.
pub(crate) fn material_for(surface: Surface) -> Option<Material> {
    choose(Os::current(), &probe(), surface)
}

/// Puts `m` behind `window`'s webviews. On failure the caller evaluates
/// [`CLEAR_SCRIPT`] in the page so it draws its CSS look again.
pub(crate) fn apply(window: &Window, m: Material) -> Result<(), String> {
    apply_native(window, m)
}

/// Applies `m` to `window`, or takes the page `eval` runs in back to the
/// CSS look when that fails.
pub(crate) fn apply_or_clear(window: &Window, m: Material, eval: impl FnOnce(&str)) {
    if let Err(e) = apply(window, m) {
        eprintln!(
            "itervox desktop: no {} material behind the page: {e}",
            m.attr()
        );
        eval(CLEAR_SCRIPT);
    }
}

#[cfg(target_os = "macos")]
fn probe() -> SystemLook {
    use objc2::runtime::AnyClass;
    use objc2_app_kit::NSWorkspace;
    use objc2_foundation::NSProcessInfo;
    let version = NSProcessInfo::processInfo().operatingSystemVersion();
    let workspace = NSWorkspace::sharedWorkspace();
    SystemLook {
        macos_major: u32::try_from(version.majorVersion).ok(),
        // Looked up at run time: the app runs on macOS 11 and later.
        glass_view: AnyClass::get(c"NSGlassEffectView").is_some(),
        windows_build: None,
        reduce_transparency: workspace.accessibilityDisplayShouldReduceTransparency(),
        increase_contrast: workspace.accessibilityDisplayShouldIncreaseContrast(),
    }
}

#[cfg(windows)]
fn probe() -> SystemLook {
    use windows_sys::Win32::UI::Accessibility::{HCF_HIGHCONTRASTON, HIGHCONTRASTW};
    use windows_sys::Win32::UI::WindowsAndMessaging::{SystemParametersInfoW, SPI_GETHIGHCONTRAST};
    use winreg::enums::HKEY_CURRENT_USER;
    use winreg::RegKey;

    // Settings › Personalization › Colors › Transparency effects.
    let transparency = RegKey::predef(HKEY_CURRENT_USER)
        .open_subkey(r"Software\Microsoft\Windows\CurrentVersion\Themes\Personalize")
        .and_then(|k| k.get_value::<u32, _>("EnableTransparency"))
        .map(|v| v != 0)
        .unwrap_or(true);
    let mut hc = HIGHCONTRASTW {
        cbSize: std::mem::size_of::<HIGHCONTRASTW>() as u32,
        ..Default::default()
    };
    // SAFETY: SPI_GETHIGHCONTRAST fills the HIGHCONTRASTW whose size
    // cbSize gives; the pointer is valid for the call.
    let ok = unsafe {
        SystemParametersInfoW(
            SPI_GETHIGHCONTRAST,
            hc.cbSize,
            (&mut hc as *mut HIGHCONTRASTW).cast(),
            0,
        )
    };
    SystemLook {
        macos_major: None,
        glass_view: false,
        windows_build: Some(windows_version::OsVersion::current().build),
        reduce_transparency: !transparency,
        increase_contrast: ok != 0 && (hc.dwFlags & HCF_HIGHCONTRASTON) != 0,
    }
}

#[cfg(not(any(target_os = "macos", windows)))]
fn probe() -> SystemLook {
    SystemLook::default()
}

#[cfg(target_os = "macos")]
fn apply_native(window: &Window, m: Material) -> Result<(), String> {
    use crate::material::VibrancyMaterial;
    use window_vibrancy::{
        apply_liquid_glass, apply_vibrancy, LiquidGlassOptions, NSGlassEffectViewStyle,
        NSVisualEffectMaterial,
    };
    match m {
        Material::LiquidGlass => apply_liquid_glass(
            window,
            LiquidGlassOptions::new(NSGlassEffectViewStyle::Regular),
        ),
        Material::Vibrancy(v) => {
            let material = match v {
                VibrancyMaterial::Sidebar => NSVisualEffectMaterial::Sidebar,
                VibrancyMaterial::HudWindow => NSVisualEffectMaterial::HudWindow,
            };
            // Follows the window's active state, like a Finder sidebar.
            apply_vibrancy(window, material, None, None)
        }
        Material::Mica => return Err("Mica is a Windows material".into()),
    }
    .map_err(|e| e.to_string())
}

#[cfg(windows)]
fn apply_native(window: &Window, m: Material) -> Result<(), String> {
    match m {
        // `None`: Mica follows the system's light or dark mode.
        Material::Mica => window_vibrancy::apply_mica(window, None).map_err(|e| e.to_string()),
        _ => Err(format!("{} is a macOS material", m.attr())),
    }
}

#[cfg(not(any(target_os = "macos", windows)))]
fn apply_native(_window: &Window, m: Material) -> Result<(), String> {
    Err(format!("no {} material on this platform", m.attr()))
}
