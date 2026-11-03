// The menu-bar app has no console window on any platform.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    itervox_desktop::run();
}
