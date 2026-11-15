import { useState } from 'react';
import { Button } from '../../ui/button';
import { defaultDeviceName, pushSupported } from '../../../lib/pwa';
import {
  useHubPushDevices,
  useRevokePushDevice,
  useSubscribePush,
} from '../../../queries/hubPhone';

// #231 — Web Push on this device: name it and turn notifications on, and
// see (and remove) every device the hub pushes to. What is pushed, and for
// which projects, is the hub's notification settings (the push channel).

export function PushDevicePanel() {
  const [name, setName] = useState(defaultDeviceName);
  const subscribe = useSubscribePush();
  const devices = useHubPushDevices();
  const revoke = useRevokePushDevice();
  const supported = pushSupported();

  return (
    <section aria-label="Push notifications" className="flex flex-col gap-3">
      <h2 className="text-theme-text text-sm font-semibold">Push notifications</h2>
      {supported ? (
        <div className="flex flex-col gap-2">
          <label className="text-theme-text-secondary flex flex-col gap-1 text-xs">
            Device name
            <input
              value={name}
              maxLength={48}
              onChange={(e) => {
                setName(e.target.value);
              }}
              className="border-theme-line bg-theme-bg-elevated text-theme-text h-10 rounded-[var(--radius-sm)] border px-3 text-sm"
            />
          </label>
          <Button
            variant="primary"
            disabled={subscribe.isPending || name.trim() === ''}
            onClick={() => {
              subscribe.mutate(name.trim());
            }}
          >
            {subscribe.isPending ? 'Turning on…' : 'Notify this device'}
          </Button>
        </div>
      ) : (
        <p className="text-theme-text-secondary text-xs" data-testid="push-unsupported">
          This browser cannot take push notifications here. On iPhone, add the hub to the Home
          Screen first (Share → Add to Home Screen) and open it from there; push also needs https.
        </p>
      )}
      <ul aria-label="Devices notified" className="flex flex-col gap-1">
        {(devices.data ?? []).map((d) => (
          <li key={d.id} className="flex items-center gap-2 text-sm">
            <span className="text-theme-text truncate">{d.name}</span>
            <span className="text-theme-muted truncate text-xs">{d.service}</span>
            <Button
              size="xs"
              variant="ghost"
              className="ml-auto"
              aria-label={`Stop notifying ${d.name}`}
              disabled={revoke.isPending}
              onClick={() => {
                revoke.mutate(d.id);
              }}
            >
              Remove
            </Button>
          </li>
        ))}
      </ul>
    </section>
  );
}
