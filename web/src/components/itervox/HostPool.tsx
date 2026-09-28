import { useMemo } from 'react';
import { useItervoxStore } from '../../store/itervoxStore';
import { EMPTY_HOSTS as EMPTY_SSH_HOSTS, EMPTY_RUNNING } from '../../utils/constants';

interface HostEntry {
  id: string;
  name: string;
  description?: string;
  kind: 'local' | 'ssh';
  active: number;
  max: number;
  disabled?: boolean;
}

function loadBarColor(pct: number): string {
  if (pct >= 90) return '#ef4444';
  if (pct >= 75) return '#f59e0b';
  return 'var(--accent)';
}

function HostTile({ host }: { host: HostEntry }) {
  // SSH tiles show only the active count — max is a global pool cap, not per-host.
  // Showing "0 / 3" on every SSH tile would imply each host can hold 3 independently.
  const showMax = host.kind === 'local';
  const pct = showMax && host.max > 0 ? Math.round((host.active / host.max) * 100) : 0;
  const counter =
    showMax && host.max > 0 ? `${String(host.active)} / ${String(host.max)}` : String(host.active);

  const kindStyle =
    host.kind === 'local'
      ? { bg: 'var(--success-soft)', color: 'var(--success-text)', label: 'Local' }
      : { bg: 'var(--accent-soft)', color: 'var(--accent-text)', label: 'SSH' };

  return (
    <div className="bg-theme-bg-elevated border-theme-line overflow-hidden rounded-xl border">
      <div className="border-theme-line flex items-center gap-2.5 border-b px-3.5 py-3">
        <span
          className={`inline-block h-1.5 w-1.5 rounded-full flex-shrink-0${host.disabled ? '' : 'animate-pulse'}`}
          style={{ background: host.disabled ? '#6b7280' : '#22c55e' }}
        />
        <span
          className="min-w-0 flex-1 truncate font-mono text-[12px] font-semibold"
          style={{ color: host.disabled ? 'var(--text-secondary)' : 'var(--text)' }}
          title={host.description ?? host.name}
        >
          {host.description ?? host.name}
        </span>
        {host.disabled ? (
          <span
            className="flex-shrink-0 rounded px-1.5 py-0.5 text-[10px] font-semibold tracking-[0.04em] uppercase"
            style={{ background: 'var(--danger-soft)', color: 'var(--danger-text)' }}
          >
            Disabled
          </span>
        ) : (
          <span
            className="flex-shrink-0 rounded px-1.5 py-0.5 text-[10px] font-semibold tracking-[0.04em] uppercase"
            style={{ background: kindStyle.bg, color: kindStyle.color }}
          >
            {kindStyle.label}
          </span>
        )}
      </div>

      <div className="px-3.5 py-3">
        <div className="mb-1.5 flex items-center justify-between">
          <span className="text-theme-text-secondary text-[11px]">Active agents</span>
          <span className="text-theme-text font-mono text-[12px] font-semibold">{counter}</span>
        </div>
        {showMax && (
          <div className="bg-theme-bg-soft h-1 overflow-hidden rounded-full">
            <div
              className="h-full rounded-full transition-all"
              style={{
                width: `${String(Math.min(pct, 100))}%`,
                background: host.disabled ? '#6b7280' : loadBarColor(pct),
              }}
            />
          </div>
        )}
        {host.kind === 'ssh' && (
          <p className="text-theme-muted mt-1.5 truncate font-mono text-[10px]">{host.name}</p>
        )}
        {host.disabled && (
          <p className="text-theme-muted mt-1.5 text-[10px] leading-snug">
            Agents route to SSH hosts
          </p>
        )}
      </div>
    </div>
  );
}

export function HostPool() {
  const running = useItervoxStore((s) => s.snapshot?.running ?? EMPTY_RUNNING);
  const sshHosts = useItervoxStore((s) => s.snapshot?.sshHosts ?? EMPTY_SSH_HOSTS);
  const maxConcurrentAgents = useItervoxStore((s) => s.snapshot?.maxConcurrentAgents ?? 0);

  const hosts = useMemo<HostEntry[]>(() => {
    const localDisabled = sshHosts.length > 0;
    const local: HostEntry = {
      id: 'local',
      name: 'local',
      kind: 'local',
      active: running.filter((r) => !r.workerHost).length,
      max: maxConcurrentAgents,
      disabled: localDisabled,
    };
    const ssh: HostEntry[] = sshHosts.map((h) => ({
      id: `ssh:${h.host}`,
      name: h.host,
      description: h.description ?? undefined,
      kind: 'ssh' as const,
      active: running.filter((r) => r.workerHost === h.host).length,
      max: maxConcurrentAgents,
    }));
    return [local, ...ssh];
  }, [running, sshHosts, maxConcurrentAgents]);

  return (
    <div>
      <span className="mb-2 block text-[11px] font-semibold tracking-[0.06em] uppercase">
        Host Pool
      </span>
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
        {hosts.map((host) => (
          <HostTile key={host.id} host={host} />
        ))}
      </div>
    </div>
  );
}
