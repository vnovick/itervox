//! Itervox menu-bar app (#128): a thin Tauri v2 shell over the itervox
//! daemons. Everything but `app` is plain Rust with unit tests that run on
//! any Unix; `app` wires it to Tauri.

pub mod cli_tool;
pub mod client;
pub mod daemon_state;
pub mod dashboard;
pub mod events;
pub mod menu;
pub mod model;
pub mod projects;
pub mod status;
pub mod supervisor;
pub mod token;
pub mod update;

mod app;
mod app_update;

pub use app::run;
