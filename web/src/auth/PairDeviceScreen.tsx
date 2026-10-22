import { useState } from 'react';
import { z } from 'zod';
import { apiRequest } from './apiRequest';
import { HUB_API_BASE } from './apiBase';
import { useTokenStore } from './tokenStore';
import { defaultDeviceName, pairingCodeFromHash } from '../lib/pwa';

/**
 * #231 — the phone's side of pairing, at /hub/pair#code=... (the link in the
 * hub page's QR code). Shown before any token exists, like TokenEntryScreen:
 * the person names the device and the one-time code is exchanged (POST
 * /hub/api/v1/pairing/exchange) for this device's own operator token, which
 * is remembered on the device (localStorage) and can be revoked from the hub
 * page. The code rides in the URL fragment, so no server ever logs it.
 */

const ExchangeSchema = z.object({ token: z.string().min(1) });

export function PairDeviceScreen({ onPaired }: { onPaired?: () => void }) {
  const [code] = useState(() => pairingCodeFromHash(window.location.hash));
  const [name, setName] = useState(defaultDeviceName);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function pair() {
    setBusy(true);
    setError(null);
    try {
      const { data } = await apiRequest(`${HUB_API_BASE}/pairing/exchange`, {
        op: 'pair this device',
        method: 'POST',
        json: { code, name: name.trim() },
        retryOnBusy: false,
      });
      useTokenStore.getState().setToken(ExchangeSchema.parse(data).token, true);
      window.history.replaceState(null, '', '/hub/pair');
      if (onPaired) onPaired();
      else window.location.assign('/hub/m');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Pairing failed.');
    } finally {
      setBusy(false);
    }
  }

  return (
    <main className="mx-auto flex min-h-screen max-w-sm flex-col justify-center gap-4 p-6">
      <h1 className="text-theme-text text-lg font-semibold">Pair this device with the hub</h1>
      {code === '' ? (
        <p role="alert" className="text-theme-danger-text text-sm">
          This link has no pairing code. Scan the QR code on the hub page again.
        </p>
      ) : (
        <form
          className="flex flex-col gap-3"
          onSubmit={(e) => {
            e.preventDefault();
            void pair();
          }}
        >
          <label className="text-theme-text-secondary flex flex-col gap-1 text-sm">
            Device name
            <input
              value={name}
              maxLength={48}
              onChange={(e) => {
                setName(e.target.value);
              }}
              className="border-theme-line bg-theme-bg-elevated text-theme-text h-11 rounded-lg border px-3 text-base"
            />
          </label>
          {error && (
            <p role="alert" className="text-theme-danger-text text-sm">
              {error}
            </p>
          )}
          <button
            type="submit"
            disabled={busy || name.trim() === ''}
            className="bg-theme-accent h-11 rounded-lg text-sm font-medium text-white disabled:opacity-50"
          >
            {busy ? 'Pairing…' : 'Pair'}
          </button>
          <p className="text-theme-muted text-xs">
            This device gets its own operator token: it can answer, approve, steer and pause. Revoke
            it any time from the hub page.
          </p>
        </form>
      )}
    </main>
  );
}
