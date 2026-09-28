// Named timing constants used across the app.
// Centralised here so tuning one value updates all affected components.

/**
 * Base delay (ms) before the first SSE reconnect attempt. Doubles on each retry
 * up to SSE_RECONNECT_MAX_MS. Read by auth/authedEventStream.ts, the single SSE
 * transport (CORE-083: these carry the live 1 s / 15 s policy).
 */
export const SSE_RECONNECT_BASE_MS = 1_000;

/** Maximum SSE reconnect backoff delay (ms). */
export const SSE_RECONNECT_MAX_MS = 15_000;

/** Delay (ms) before the running-sessions table clears stale rows after agents go idle. */
export const LOG_STABLE_DELAY_MS = 5_000;

/** Duration (ms) the "Saved successfully" banner stays visible after a settings save. */
export const SAVE_OK_BANNER_MS = 3_000;

/** Duration (ms) before a success/info toast auto-dismisses. Error toasts are sticky. */
export const TOAST_DISMISS_MS = 4_000;

/** Maximum toasts visible per live region (errors / success+info) before the oldest is evicted. */
export const TOAST_REGION_CAP = 3;

/** CORE-089 — interval (ms) at which the "Can't reach the daemon" screen re-probes on its own. */
export const SERVER_DOWN_RETRY_MS = 5_000;

/** M6-close — upper bound on each AuthGate probe fetch (health, state); a hung probe fails as "server down". */
export const AUTH_PROBE_TIMEOUT_MS = 5_000;
