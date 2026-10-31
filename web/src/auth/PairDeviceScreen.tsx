import { useState } from 'react';
import { z } from 'zod';
import { apiRequest } from './apiRequest';
import { HUB_API_BASE } from './apiBase';
import { useTokenStore } from './tokenStore';
import { defaultDeviceName, pairingCodeFromHash } from '../lib/pwa';
import { StatusCard } from '../components/common/StatusCard';
import { plainButtonClass } from '../components/ui/button/buttonRecipe';
import { FIELD_CLASS } from '../components/ui/Input/fieldClass';

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
    <main className="flex min-h-screen items-center justify-center p-4 sm:p-6">
      <StatusCard eyebrow="From the hub's QR code" title="Pair this device with the hub">
        {code === '' ? (
          <p role="alert" className="text-theme-danger-text">
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
            <div className="flex flex-col">
              <label htmlFor="pair-device-name" className="field-label">
                Device name
              </label>
              <input
                id="pair-device-name"
                value={name}
                maxLength={48}
                onChange={(e) => {
                  setName(e.target.value);
                }}
                className={`${FIELD_CLASS} h-11 text-base`}
              />
            </div>
            <p>
              This device gets its own operator token: it can answer, approve, steer and pause.
              Revoke it any time from the hub page.
            </p>
            {error && (
              <p role="alert" className="text-theme-danger-text">
                {error}
              </p>
            )}
            <button
              type="submit"
              disabled={busy || name.trim() === ''}
              className={plainButtonClass('primary', 'lg', 'self-start')}
            >
              {busy ? 'Pairing…' : 'Pair'}
            </button>
          </form>
        )}
      </StatusCard>
    </main>
  );
}
