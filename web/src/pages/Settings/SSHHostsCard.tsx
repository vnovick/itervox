import { useState } from 'react';
import { useItervoxStore } from '../../store/itervoxStore';
import { useSettingsActions } from '../../hooks/useSettingsActions';
import { AddSSHHostModal } from './AddSSHHostModal';
import { EMPTY_HOSTS } from '../../utils/constants';
import { buttonClass } from '../../components/ui/button/buttonStyles';

const STRATEGIES: { id: string; label: string; desc: string; available: boolean }[] = [
  { id: 'round-robin', label: 'Round Robin', desc: 'Cycle hosts in order', available: true },
  {
    id: 'least-loaded',
    label: 'Least Loaded',
    desc: 'Route to host with fewest active agents',
    available: true,
  },
];

export function SSHHostsCard() {
  const hosts = useItervoxStore((s) => s.snapshot?.sshHosts ?? EMPTY_HOSTS);
  const strategy = useItervoxStore((s) => s.snapshot?.dispatchStrategy ?? 'round-robin');
  const { addSSHHost, removeSSHHost, setDispatchStrategy } = useSettingsActions();
  const [addOpen, setAddOpen] = useState(false);
  const [removingHost, setRemovingHost] = useState<string | null>(null);

  const handleRemove = async (host: string) => {
    setRemovingHost(host);
    await removeSSHHost(host);
    setRemovingHost(null);
  };

  return (
    <>
      <div className="space-y-3">
        <p className="text-theme-text-secondary text-[13px]">
          {hosts.length === 0
            ? 'No SSH hosts configured — agents run locally on this machine.'
            : `${String(hosts.length)} host${hosts.length === 1 ? '' : 's'}`}
        </p>

        {/* Dispatch strategy — only shown when there are hosts */}
        {hosts.length > 0 && (
          <div>
            <p className="field-label">Dispatch strategy</p>
            <div className="grid grid-cols-2 gap-2">
              {STRATEGIES.map((s) => (
                <button
                  key={s.id}
                  type="button"
                  aria-pressed={strategy === s.id}
                  onClick={() => {
                    void setDispatchStrategy(s.id);
                  }}
                  className="choice-card flex-col gap-0.5"
                >
                  <span className="text-theme-text text-[13px] font-semibold">{s.label}</span>
                  <span className="text-theme-text-secondary text-xs leading-[1.4]">{s.desc}</span>
                </button>
              ))}
            </div>
          </div>
        )}

        {hosts.length > 0 && (
          <ul className="space-y-2" aria-label="SSH hosts">
            {hosts.map((h) => (
              <li
                key={h.host}
                className="glass-card flex items-center gap-2 rounded-[14px] px-3 py-2 text-[13px]"
              >
                <div className="min-w-0 flex-1">
                  <span className="text-theme-text font-mono">{h.host}</span>
                  {h.description && (
                    <span className="text-theme-text-secondary ml-2 text-xs">{h.description}</span>
                  )}
                </div>
                <button
                  type="button"
                  onClick={() => {
                    void handleRemove(h.host);
                  }}
                  disabled={removingHost === h.host}
                  className={buttonClass(
                    'secondary',
                    'xs',
                    'text-theme-danger-text disabled:cursor-wait',
                    true,
                  )}
                >
                  {removingHost === h.host ? 'Removing…' : 'Remove'}
                </button>
              </li>
            ))}
          </ul>
        )}

        <button
          type="button"
          onClick={() => {
            setAddOpen(true);
          }}
          className={buttonClass('secondary', 'md', undefined, true)}
        >
          + Add host
        </button>
      </div>

      <AddSSHHostModal
        isOpen={addOpen}
        onClose={() => {
          setAddOpen(false);
        }}
        onAdd={addSSHHost}
      />
    </>
  );
}
