//! Itervox menu-bar app (#128): a thin Tauri v2 shell over the itervox
//! daemons, local and remote (#156). Everything but `app`, `app_remote` and
//! `app_update` is plain Rust with unit tests that run on any Unix; those
//! three wire it to Tauri.

pub mod cli_tool;
pub mod client;
pub mod daemon_state;
pub mod dashboard;
pub mod events;
pub mod menu;
pub mod model;
pub mod projects;
pub mod prompt;
pub mod remote;
pub mod remote_model;
pub mod remote_ops;
pub mod sessions;
pub mod shellpath;
pub mod status;
pub mod supervisor;
pub mod sysevents;
pub mod token;
pub mod tunnel;
pub mod update;

mod app;
mod app_remote;
mod app_update;

pub use app::run;
