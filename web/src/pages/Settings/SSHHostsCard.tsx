import { useState } from 'react';
import { useItervoxStore } from '../../store/itervoxStore';
import { useSettingsActions } from '../../hooks/useSettingsActions';
import { AddSSHHostModal } from './AddSSHHostModal';
import { EMPTY_HOSTS } from '../../utils/constants';

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
      <div className="bg-theme-bg-elevated border-theme-line overflow-hidden rounded-xl border">
        {/* Header */}
        <div className="border-theme-line flex items-center justify-between border-b px-[18px] py-3.5">
          <div>
            <span className="text-theme-text text-[15px] font-semibold">SSH Hosts</span>
            <span className="ml-2 text-[11px]">
              {hosts.length === 0
                ? 'running locally'
                : `${String(hosts.length)} host${hosts.length === 1 ? '' : 's'}`}
            </span>
          </div>
          <button
            onClick={() => {
              setAddOpen(true);
            }}
            style={{
              padding: '5px 12px',
              borderRadius: 6,
              fontSize: 12,
              fontWeight: 600,
              cursor: 'pointer',
              background: 'var(--accent)',
              color: '#fff',
              border: 'none',
            }}
          >
            + Add host
          </button>
        </div>

        {/* Host list */}
        {hosts.length === 0 ? (
          <div className="text-theme-muted px-[18px] py-5 text-[13px]">
            No SSH hosts configured — agents run locally on this machine.
          </div>
        ) : (
          <ul className="border-theme-line divide-y">
            {hosts.map((h) => (
              <li key={h.host} className="flex items-center justify-between px-[18px] py-3">
                <div className="min-w-0">
                  <span className="text-theme-text font-mono text-[13px]">{h.host}</span>
                  {h.description && (
                    <span className="text-theme-muted ml-2 text-[12px]">{h.description}</span>
                  )}
                </div>
                <button
                  onClick={() => {
                    void handleRemove(h.host);
                  }}
                  disabled={removingHost === h.host}
                  className="ml-4 flex-shrink-0 text-[12px] transition-opacity"
                  style={{
                    color: 'var(--danger-text)',
                    background: 'transparent',
                    border: 'none',
                    cursor: removingHost === h.host ? 'wait' : 'pointer',
                    opacity: removingHost === h.host ? 0.5 : 1,
                  }}
                >
                  {removingHost === h.host ? 'Removing…' : 'Remove'}
                </button>
              </li>
            ))}
          </ul>
        )}

        {/* Dispatch strategy — only shown when there are hosts */}
        {hosts.length > 0 && (
          <div className="border-theme-line border-t px-[18px] py-4">
            <p className="text-theme-text-secondary mb-2.5 text-[12px] font-medium">
              Dispatch strategy
            </p>
            <div className="flex gap-2">
              {STRATEGIES.map((s) => (
                <button
                  key={s.id}
                  onClick={() => {
                    void setDispatchStrategy(s.id);
                  }}
                  className="flex-1 rounded-lg border-2 px-3 py-2.5 text-left transition-all"
                  style={{
                    borderColor: strategy === s.id ? 'var(--accent)' : 'var(--line)',
                    background: strategy === s.id ? 'rgba(99,102,241,0.06)' : 'transparent',
                  }}
                >
                  <div className="text-theme-text text-[12px] font-semibold">{s.label}</div>
                  <div className="text-theme-text-secondary mt-0.5 text-[11px] leading-[1.4]">
                    {s.desc}
                  </div>
                </button>
              ))}
            </div>
          </div>
        )}
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
