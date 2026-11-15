import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  base64UrlToBytes,
  defaultDeviceName,
  installHubWebApp,
  MANIFEST_HREF,
  pairingCodeFromHash,
  SERVICE_WORKER_SCOPE,
  SERVICE_WORKER_URL,
} from '../pwa';
import { resolveApiContext } from '../../auth/apiBase';

function freshDocument(): Document {
  return document.implementation.createHTMLDocument('t');
}

afterEach(() => {
  vi.restoreAllMocks();
});

// #231 — the hub as an installable web app: manifest + service worker.
describe('installHubWebApp', () => {
  it('registers the push service worker for /hub/ and links the manifest on the hub', async () => {
    const reg = { scope: '/hub/' } as unknown as ServiceWorkerRegistration;
    const register = vi.fn().mockResolvedValue(reg);
    const doc = freshDocument();
    const nav = { serviceWorker: { register } } as unknown as Pick<Navigator, 'serviceWorker'>;

    await expect(installHubWebApp(resolveApiContext('/hub/m'), nav, doc)).resolves.toBe(reg);
    expect(register).toHaveBeenCalledTimes(1);
    expect(register).toHaveBeenCalledWith(SERVICE_WORKER_URL, { scope: SERVICE_WORKER_SCOPE });
    expect(SERVICE_WORKER_URL).toBe('/sw.js');
    expect(SERVICE_WORKER_SCOPE).toBe('/hub/');
    expect(doc.head.querySelector('link[rel="manifest"]')?.getAttribute('href')).toBe(
      MANIFEST_HREF,
    );
    expect(doc.head.querySelector('link[rel="apple-touch-icon"]')).not.toBeNull();

    // Idempotent: a second call adds no second manifest link.
    await installHubWebApp(resolveApiContext('/hub/p/alpha/m/ENG-1'), nav, doc);
    expect(doc.head.querySelectorAll('link[rel="manifest"]')).toHaveLength(1);
    expect(register).toHaveBeenCalledTimes(2);
  });

  it('leaves a daemon dashboard alone', async () => {
    const register = vi.fn();
    const doc = freshDocument();
    const nav = { serviceWorker: { register } } as unknown as Pick<Navigator, 'serviceWorker'>;
    await expect(installHubWebApp(resolveApiContext('/'), nav, doc)).resolves.toBeNull();
    expect(register).not.toHaveBeenCalled();
    expect(doc.head.querySelector('link[rel="manifest"]')).toBeNull();
  });

  it('survives a browser without service workers or a failed registration', async () => {
    const doc = freshDocument();
    await expect(installHubWebApp(resolveApiContext('/hub/'), {}, doc)).resolves.toBeNull();
    expect(doc.head.querySelector('link[rel="manifest"]')).not.toBeNull();
    const register = vi.fn().mockRejectedValue(new Error('insecure origin'));
    const nav = { serviceWorker: { register } } as unknown as Pick<Navigator, 'serviceWorker'>;
    await expect(installHubWebApp(resolveApiContext('/hub/'), nav, doc)).resolves.toBeNull();
  });
});

describe('pwa helpers', () => {
  it('decodes a base64url VAPID key', () => {
    expect(Array.from(base64UrlToBytes('AQID_-8'))).toEqual([1, 2, 3, 255, 239]);
  });

  it('reads the pairing code from the fragment', () => {
    expect(pairingCodeFromHash('#code=ABC234')).toBe('ABC234');
    expect(pairingCodeFromHash('')).toBe('');
  });

  it('names the device from the browser', () => {
    expect(defaultDeviceName('Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)')).toBe(
      'iPhone',
    );
    expect(defaultDeviceName('Mozilla/5.0 (Linux; Android 15; Pixel 9)')).toBe('Android phone');
    expect(defaultDeviceName('Mozilla/5.0 (X11; Linux x86_64)')).toBe('This browser');
  });
});
