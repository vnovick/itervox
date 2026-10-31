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

function loadBarClass(pct: number): string {
  if (pct >= 90) return 'bg-theme-danger';
  if (pct >= 75) return 'bg-theme-warning';
  return 'bg-theme-accent';
}

function HostTile({ host }: { host: HostEntry }) {
  // SSH tiles show only the active count — max is a global pool cap, not per-host.
  // Showing "0 / 3" on every SSH tile would imply each host can hold 3 independently.
  const showMax = host.kind === 'local';
  const pct = showMax && host.max > 0 ? Math.round((host.active / host.max) * 100) : 0;
  const counter =
    showMax && host.max > 0 ? `${String(host.active)} / ${String(host.max)}` : String(host.active);

  const kind =
    host.kind === 'local'
      ? { tone: 'pill-ok', label: 'Local' }
      : { tone: 'pill-accent', label: 'SSH' };

  return (
    <div className="glass-card overflow-hidden">
      <div className="flex items-center gap-2.5 px-3.5 pt-3 pb-1.5">
        <span
          aria-hidden="true"
          className={`status-dot ${host.disabled ? 'dot-idle' : 'dot-ok animate-pulse'}`}
        />
        <span
          className={`min-w-0 flex-1 truncate font-mono text-[12px] font-semibold ${
            host.disabled ? 'text-theme-text-secondary' : 'text-theme-text'
          }`}
          title={host.description ?? host.name}
        >
          {host.description ?? host.name}
        </span>
        {host.disabled ? (
          <span className="tag pill-danger flex-shrink-0">Disabled</span>
        ) : (
          <span className={`tag flex-shrink-0 ${kind.tone}`}>{kind.label}</span>
        )}
      </div>

      <div className="px-3.5 pt-1.5 pb-3">
        <div className="mb-1.5 flex items-center justify-between">
          <span className="text-theme-text-secondary text-xs">Active agents</span>
          <span className="text-theme-text font-mono text-[12px] font-semibold">{counter}</span>
        </div>
        {showMax && (
          <div className="bg-theme-bg-soft h-1 overflow-hidden rounded-full">
            <div
              className={`h-full rounded-full transition-all ${
                host.disabled ? 'bg-theme-dot-idle' : loadBarClass(pct)
              }`}
              style={{ width: `${String(Math.min(pct, 100))}%` }}
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
    <section
      aria-labelledby="host-pool-heading"
      className="glass-panel flex flex-col gap-3 px-3 py-4 sm:px-[18px]"
    >
      <div className="flex items-center gap-2 px-1">
        <h3 id="host-pool-heading" className="section-title">
          Host pool
        </h3>
        <span className="count-pill">{hosts.length}</span>
      </div>
      <div className="grid grid-cols-2 gap-2.5 sm:grid-cols-3 lg:grid-cols-4">
        {hosts.map((host) => (
          <HostTile key={host.id} host={host} />
        ))}
      </div>
    </section>
  );
}
