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
 *
 * #231 — the API ORIGIN. The same bundle also runs hosted on another origin
 * (a static host in front of a hub reached over Tailscale or a tunnel). The
 * origin every API URL is sent to comes from, first match wins:
 *   1. `<meta name="itervox-api-origin" content="https://hub.example">` in
 *      the served index.html — a runtime setting of whoever hosts the page;
 *   2. `VITE_API_ORIGIN` at build time;
 *   3. none: the page's own origin (embedded in a daemon or the hub).
 * Never the page URL: a query parameter could otherwise send the stored
 * token to an origin the person never chose. The hub (or daemon) must list
 * the page's origin in `server.allowed_origins`.
 */

/** The prefix every frontend API URL is written with, and the default base. */
export const DEFAULT_API_BASE = '/api/v1';

/** The hub's own API (#228). */
export const HUB_API_BASE = '/hub/api/v1';

/** Where the dashboard's API lives. */
export type ApiContext =
  /** A daemon serving its own dashboard. */
  | { mode: 'daemon'; apiBase: string; basename: string }
  /**
   * The hub's own pages: the project switcher at /hub/, the phone layout at
   * /hub/m (#231) and the pairing page at /hub/pair (#231).
   */
  | { mode: 'hub'; apiBase: string; basename: string; view: HubView }
  /** One project's dashboard through the hub at /hub/p/{project}/. */
  | { mode: 'hubProject'; project: string; apiBase: string; basename: string };

/** Which hub page /hub/... is (#231). */
export type HubView = 'switcher' | 'mobile' | 'pair';

/** A hub project name: the hub's slug alphabet, never starting with a dot. */
const PROJECT_RE = /^[a-z0-9_-][a-z0-9._-]*$/;
const HUB_PROJECT_PATH_RE = /^\/hub\/p\/([^/]+)(?:\/|$)/;
const HUB_ROOT_PATH_RE = /^\/hub\/?$/;
const HUB_MOBILE_PATH_RE = /^\/hub\/m\/?$/;
const HUB_PAIR_PATH_RE = /^\/hub\/pair\/?$/;

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
  const view: HubView | null = HUB_ROOT_PATH_RE.test(pathname)
    ? 'switcher'
    : HUB_MOBILE_PATH_RE.test(pathname)
      ? 'mobile'
      : HUB_PAIR_PATH_RE.test(pathname)
        ? 'pair'
        : null;
  if (view) {
    return { mode: 'hub', apiBase: HUB_API_BASE, basename: '/hub', view };
  }
  return { mode: 'daemon', apiBase: validBase(buildBase) ?? DEFAULT_API_BASE, basename: '' };
}

function initialContext(): ApiContext {
  const pathname = typeof window === 'undefined' ? '/' : window.location.pathname;
  return resolveApiContext(pathname, import.meta.env.VITE_API_BASE as string | undefined);
}

let current: ApiContext = initialContext();

/**
 * A configured API origin is used only when it is exactly an http(s)
 * origin (no path, query, credentials or fragment); anything else is ignored.
 */
export function validOrigin(raw: string | null | undefined): string | null {
  const value = raw?.trim() ?? '';
  if (value === '') return null;
  try {
    const u = new URL(value);
    if (u.protocol !== 'https:' && u.protocol !== 'http:') return null;
    if (
      u.username ||
      u.password ||
      u.search ||
      u.hash ||
      (u.pathname !== '/' && u.pathname !== '')
    ) {
      return null;
    }
    if (value.replace(/\/+$/, '').toLowerCase() !== u.origin.toLowerCase()) return null;
    return u.origin;
  } catch {
    return null;
  }
}

function initialOrigin(): string {
  const meta =
    typeof document === 'undefined'
      ? null
      : document.querySelector('meta[name="itervox-api-origin"]')?.getAttribute('content');
  const configured =
    validOrigin(meta) ?? validOrigin(import.meta.env.VITE_API_ORIGIN as string | undefined);
  if (!configured || (typeof window !== 'undefined' && configured === window.location.origin)) {
    return '';
  }
  return configured;
}

let origin: string = initialOrigin();

/** The API origin ('' = the page's own origin). */
export function apiOrigin(): string {
  return origin;
}

/** Sets the API origin (tests); an invalid value means same-origin. */
export function configureApiOrigin(raw: string | null): string {
  origin = validOrigin(raw) ?? '';
  return origin;
}

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
    return origin + current.apiBase + url.slice(DEFAULT_API_BASE.length);
  }
  // #231: the hub's own API and its per-project proxy follow the origin too.
  if (/^\/hub\/(?:api\/v1|p\/[^/]+\/api\/v1)(?:[/?#]|$)/.test(url)) {
    return origin + url;
  }
  return url;
}

/** The hub proxy URL of a project's `/api/v1/...` path (#231). */
export function hubProjectApiUrl(project: string, path: string): string {
  const rest = path.startsWith(DEFAULT_API_BASE) ? path.slice(DEFAULT_API_BASE.length) : path;
  return apiUrl(`/hub/p/${encodeURIComponent(project)}/api/v1${rest}`);
}

/** The unauthenticated health probe AuthGate starts with. */
export function healthUrl(): string {
  return current.mode === 'daemon' ? apiUrl('/api/v1/health') : apiUrl(`${HUB_API_BASE}/health`);
}

/** The authenticated probe AuthGate and the token screen validate with. */
export function authProbeUrl(): string {
  return current.mode === 'hub' ? apiUrl(`${HUB_API_BASE}/whoami`) : apiUrl('/api/v1/state');
}

/** React Router's basename for this page ('' at the root). */
export function routerBasename(): string {
  return current.basename;
}
