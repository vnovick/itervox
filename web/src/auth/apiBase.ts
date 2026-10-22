/**
 * #228 — the configurable API base path. The ONE place that decides where the
 * dashboard's API lives; `authedFetch` and `openAuthedEventStream` map every
 * URL through `apiUrl`, so call sites keep writing `/api/v1/...`.
 *
 * Where the base comes from (first match wins):
 *   1. The page URL, at runtime. The hub serves the dashboard at
 *      `/hub/p/{project}/...` and proxies `/hub/p/{project}/api/v1/...` to that
 *      project's daemon, so under that prefix the base is the proxied path and
 *      React Router's basename is `/hub/p/{project}`. `/hub` and `/hub/` are the
 *      hub's own page (the project switcher), whose API is `/hub/api/v1`.
 *   2. `VITE_API_BASE` at build time (a path starting with `/`).
 *   3. `DEFAULT_API_BASE` (`/api/v1`): a daemon serving its own dashboard.
 *
 * Only same-origin paths are accepted. A base taken from the URL could
 * otherwise send the stored bearer token to another origin.
 */

/** The prefix every frontend API URL is written with, and the default base. */
export const DEFAULT_API_BASE = '/api/v1';

/** The hub's own API (#228). */
export const HUB_API_BASE = '/hub/api/v1';

/** Where the dashboard's API lives. */
export type ApiContext =
  /** A daemon serving its own dashboard. */
  | { mode: 'daemon'; apiBase: string; basename: string }
  /** The hub's project switcher at /hub/. */
  | { mode: 'hub'; apiBase: string; basename: string }
  /** One project's dashboard through the hub at /hub/p/{project}/. */
  | { mode: 'hubProject'; project: string; apiBase: string; basename: string };

/** A hub project name: the hub's slug alphabet, never starting with a dot. */
const PROJECT_RE = /^[a-z0-9_-][a-z0-9._-]*$/;
const HUB_PROJECT_PATH_RE = /^\/hub\/p\/([^/]+)(?:\/|$)/;
const HUB_ROOT_PATH_RE = /^\/hub\/?$/;

/** A build-time base is used only when it is a plain absolute path. */
function validBase(raw: string | undefined): string | null {
  if (!raw) return null;
  const base = raw.trim().replace(/\/+$/, '');
  return /^\/[A-Za-z0-9._~/-]*$/.test(base) && !base.includes('//') && !base.includes('..')
    ? base
    : null;
}

/** Resolves the API context of a page at pathname. */
export function resolveApiContext(pathname: string, buildBase?: string): ApiContext {
  const project = HUB_PROJECT_PATH_RE.exec(pathname)?.[1];
  if (project !== undefined && PROJECT_RE.test(project)) {
    const basename = `/hub/p/${project}`;
    return { mode: 'hubProject', project, apiBase: `${basename}/api/v1`, basename };
  }
  if (HUB_ROOT_PATH_RE.test(pathname)) {
    return { mode: 'hub', apiBase: HUB_API_BASE, basename: '/hub' };
  }
  return { mode: 'daemon', apiBase: validBase(buildBase) ?? DEFAULT_API_BASE, basename: '' };
}

function initialContext(): ApiContext {
  const pathname = typeof window === 'undefined' ? '/' : window.location.pathname;
  return resolveApiContext(pathname, import.meta.env.VITE_API_BASE as string | undefined);
}

let current: ApiContext = initialContext();

/** The API context this page resolved at load. */
export function getApiContext(): ApiContext {
  return current;
}

/** Re-resolves the context (tests, or after a full-page navigation). */
export function configureApiContext(pathname: string, buildBase?: string): ApiContext {
  current = resolveApiContext(pathname, buildBase);
  return current;
}

/**
 * Maps a URL written as `/api/v1/...` onto the configured base. Anything else
 * (another path, an absolute URL) is returned unchanged.
 */
export function apiUrl(url: string): string {
  if (url === DEFAULT_API_BASE || /^\/api\/v1[/?#]/.test(url)) {
    return current.apiBase + url.slice(DEFAULT_API_BASE.length);
  }
  return url;
}

/** The unauthenticated health probe AuthGate starts with. */
export function healthUrl(): string {
  return current.mode === 'daemon' ? apiUrl('/api/v1/health') : `${HUB_API_BASE}/health`;
}

/** The authenticated probe AuthGate and the token screen validate with. */
export function authProbeUrl(): string {
  return current.mode === 'hub' ? `${HUB_API_BASE}/whoami` : apiUrl('/api/v1/state');
}

/** React Router's basename for this page ('' at the root). */
export function routerBasename(): string {
  return current.basename;
}
