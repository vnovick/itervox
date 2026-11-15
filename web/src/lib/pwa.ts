import { getApiContext, type ApiContext } from '../auth/apiBase';

/**
 * #231 — the hub as an installable web app. On the hub's pages (/hub/...,
 * never a daemon's own dashboard) the page links the web app manifest and
 * registers the push-only service worker (public/sw.js) for the /hub/ scope.
 * Both files ship in the embedded bundle; the hub serves them like any
 * other static file.
 */

export const MANIFEST_HREF = '/manifest.webmanifest';
export const SERVICE_WORKER_URL = '/sw.js';
export const SERVICE_WORKER_SCOPE = '/hub/';
const THEME_COLOR = '#111113';

function ensureHeadElement(doc: Document, selector: string, create: () => HTMLElement): void {
  if (doc.head.querySelector(selector)) return;
  doc.head.appendChild(create());
}

/** Adds the manifest, theme colour and home-screen icon links once. */
export function linkWebAppManifest(doc: Document = document): void {
  ensureHeadElement(doc, 'link[rel="manifest"]', () => {
    const link = doc.createElement('link');
    link.rel = 'manifest';
    link.href = MANIFEST_HREF;
    return link;
  });
  ensureHeadElement(doc, 'meta[name="theme-color"]', () => {
    const meta = doc.createElement('meta');
    meta.name = 'theme-color';
    meta.content = THEME_COLOR;
    return meta;
  });
  ensureHeadElement(doc, 'link[rel="apple-touch-icon"]', () => {
    const link = doc.createElement('link');
    link.rel = 'apple-touch-icon';
    link.href = '/icons/apple-touch-icon.png';
    return link;
  });
}

type ServiceWorkerHost = Pick<Navigator, 'serviceWorker'> | { serviceWorker?: undefined };

/**
 * Links the manifest and registers the service worker on the hub's pages.
 * Resolves to the registration, or null on a daemon's dashboard, without
 * service worker support, or when registration fails (an insecure origin:
 * service workers need https or localhost).
 */
export async function installHubWebApp(
  ctx: ApiContext = getApiContext(),
  nav: ServiceWorkerHost = navigator,
  doc: Document = document,
): Promise<ServiceWorkerRegistration | null> {
  if (ctx.mode === 'daemon') return null;
  linkWebAppManifest(doc);
  if (!nav.serviceWorker) return null;
  try {
    return await nav.serviceWorker.register(SERVICE_WORKER_URL, { scope: SERVICE_WORKER_SCOPE });
  } catch {
    return null;
  }
}

/** Decodes a base64url VAPID key into the bytes PushManager.subscribe wants. */
export function base64UrlToBytes(value: string): Uint8Array<ArrayBuffer> {
  const b64 = value.replace(/-/g, '+').replace(/_/g, '/');
  const padded = b64 + '='.repeat((4 - (b64.length % 4)) % 4);
  const raw = atob(padded);
  const out = new Uint8Array(new ArrayBuffer(raw.length));
  for (let i = 0; i < raw.length; i += 1) out[i] = raw.charCodeAt(i);
  return out;
}

/** Whether this browser can take Web Push at all. */
export function pushSupported(win: Window = window): boolean {
  return 'serviceWorker' in win.navigator && 'PushManager' in win && 'Notification' in win;
}

/**
 * Asks for notification permission and returns this browser's push
 * subscription for the hub's VAPID key (an existing one is reused).
 */
export async function subscribeBrowserPush(vapidPublicKey: string): Promise<PushSubscriptionJSON> {
  const permission = await Notification.requestPermission();
  if (permission !== 'granted') {
    throw new Error('Notifications are blocked for this site. Allow them in the browser settings.');
  }
  const reg = await navigator.serviceWorker.ready;
  const existing = await reg.pushManager.getSubscription();
  const sub =
    existing ??
    (await reg.pushManager.subscribe({
      userVisibleOnly: true,
      applicationServerKey: base64UrlToBytes(vapidPublicKey),
    }));
  return sub.toJSON();
}

/** A default device name from the browser. */
export function defaultDeviceName(ua: string = navigator.userAgent): string {
  if (/iPhone/.test(ua)) return 'iPhone';
  if (/iPad/.test(ua)) return 'iPad';
  if (/Android/.test(ua)) return 'Android phone';
  return 'This browser';
}

/** The pairing code in a /hub/pair#code=... fragment ('' if none). */
export function pairingCodeFromHash(hash: string): string {
  return new URLSearchParams(hash.replace(/^#/, '')).get('code')?.trim() ?? '';
}
