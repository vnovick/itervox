import { useState } from 'react';
import { Button } from '../../ui/button';
import { validOrigin } from '../../../auth/apiBase';
import { Card } from '../../ui/Card/Card';
import { QrCode } from './QrCode';
import { PushDevicePanel } from './PushDevicePanel';
import { useCreatePairing, useHubDevices, useRevokeHubDevice } from '../../../queries/hubPhone';

// #231 — the hub page's Phones view (admin): pair a phone with a one-time
// QR code (it expires in ten minutes and works once; the phone gets its own
// operator token), list the paired devices and revoke one (its token and
// its push notifications stop at once). The phone must reach this page's
// address: on the LAN, over Tailscale or through your own tunnel.

export function HubPairingPanel() {
  const pairing = useCreatePairing();
  const devices = useHubDevices();
  const revoke = useRevokeHubDevice();
  // The address the phone opens: this page's by default, or the one the
  // phone reaches the hub at (a Tailscale or tunnel URL) when this page is
  // open on loopback.
  const [address, setAddress] = useState(window.location.origin);
  const base = validOrigin(address);
  const loopback = base !== null && /^https?:\/\/(localhost|127\.|\[::1\])/.test(base);
  const link = pairing.data && base ? `${base}${pairing.data.path}` : '';

  return (
    <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
      <Card padding="md" className="flex flex-col gap-3">
        <h2 className="text-theme-text text-sm font-semibold">Pair a phone</h2>
        <p className="text-theme-text-secondary text-xs">
          Scan with the phone's camera. The phone opens the hub at the address below, so it must
          reach it: same network, Tailscale or your own tunnel. Notifications and installing need
          https.
        </p>
        <label className="text-theme-text-secondary flex flex-col gap-1 text-xs">
          Hub address on the phone
          <input
            value={address}
            onChange={(e) => {
              setAddress(e.target.value);
            }}
            placeholder="https://my-mac.tailnet.ts.net"
            className="border-theme-line bg-theme-bg-elevated text-theme-text h-9 rounded-[var(--radius-sm)] border px-2 font-mono text-xs"
          />
        </label>
        {base === null && (
          <p role="alert" className="text-theme-danger-text text-xs">
            Enter an address like https://my-mac.tailnet.ts.net (no path).
          </p>
        )}
        {loopback && (
          <p className="text-theme-warning-text text-xs" data-testid="pairing-loopback">
            This address only works on this computer. Use the one your phone reaches the hub at.
          </p>
        )}
        {pairing.data && link ? (
          <div className="flex flex-col items-start gap-2">
            <QrCode value={link} label="Pairing QR code" />
            <p className="text-theme-muted text-xs">
              Works once, until {new Date(pairing.data.expiresAt).toLocaleTimeString()}.
            </p>
            <a href={link} className="text-theme-accent-strong text-xs break-all underline">
              {link}
            </a>
          </div>
        ) : null}
        <Button
          variant="primary"
          className="self-start"
          disabled={pairing.isPending}
          onClick={() => {
            pairing.mutate();
          }}
        >
          {pairing.data ? 'New code' : 'Show pairing QR code'}
        </Button>
      </Card>
      <Card padding="md" className="flex flex-col gap-3">
        <h2 className="text-theme-text text-sm font-semibold">Paired devices</h2>
        {devices.data && devices.data.length === 0 && (
          <p className="text-theme-muted text-xs">No phone is paired yet.</p>
        )}
        <ul aria-label="Paired devices" className="flex flex-col gap-1">
          {(devices.data ?? []).map((d) => (
            <li key={d.id} className="flex items-center gap-2 text-sm">
              <span className="text-theme-text truncate">{d.name}</span>
              <span className="text-theme-muted text-xs">
                {d.pushDevices > 0 ? 'notifications on' : 'no notifications'}
              </span>
              <Button
                size="xs"
                variant="danger"
                className="ml-auto"
                aria-label={`Revoke ${d.name}`}
                disabled={revoke.isPending}
                onClick={() => {
                  revoke.mutate(d.id);
                }}
              >
                Revoke
              </Button>
            </li>
          ))}
        </ul>
        <PushDevicePanel />
      </Card>
    </div>
  );
}
