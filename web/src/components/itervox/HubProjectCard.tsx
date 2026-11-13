import { Card } from '../ui/Card/Card';
import { buttonClass } from '../ui/button/buttonStyles';
import { hubProjectHref } from '../../queries/hub';
import type { HubProject } from '../../types/schemas';

// #228 — one hub member on the hub's project switcher.

function countsLine(p: HubProject): string {
  const c = p.summary?.counts;
  if (!c) return '';
  const parts = [`${String(c.running ?? 0)} running`];
  if (c.inputRequired) parts.push(`${String(c.inputRequired)} need input`);
  if (c.retrying) parts.push(`${String(c.retrying)} retrying`);
  if (p.summary?.paused) parts.push('dispatch paused');
  return parts.length === 1 && !c.running ? 'Nothing running' : parts.join(' · ');
}

type Tone = 'ok' | 'warn' | 'danger' | 'idle';

/** The card's status: the member's health when it is not ok, else what it needs. */
function projectStatus(p: HubProject): { label: string; tone: Tone } {
  if (p.health === 'unauthorized') return { label: p.health, tone: 'warn' };
  if (p.health !== 'ok') return { label: p.health, tone: 'danger' };
  const c = p.summary?.counts;
  if (c?.inputRequired) return { label: 'Needs you', tone: 'warn' };
  if (c?.running) return { label: 'Healthy', tone: 'ok' };
  return { label: 'Idle', tone: 'idle' };
}

const TONE_TEXT: Record<Tone, string> = {
  ok: 'text-theme-success-text',
  warn: 'text-theme-warning-text',
  danger: 'text-theme-danger-text',
  idle: 'text-theme-text-secondary',
};

export function HubProjectCard({ project }: { project: HubProject }) {
  const counts = countsLine(project);
  const status = projectStatus(project);
  return (
    <Card variant="panel" padding="none" className="flex h-full flex-col gap-2 px-5 py-[18px]">
      <div className="flex min-w-0 items-center gap-2">
        <span aria-hidden="true" className={`status-dot dot-${status.tone}`} />
        <span className="text-theme-text truncate text-[17px] font-semibold">{project.name}</span>
        <span
          className={`ml-auto shrink-0 text-xs font-semibold ${TONE_TEXT[status.tone]}`}
          title={`health: ${project.health}`}
        >
          {status.label}
        </span>
      </div>
      <p
        className="text-theme-text-secondary truncate font-mono text-xs"
        title={project.projectDir}
      >
        {project.projectDir} · {project.version}
      </p>
      {counts && <p className="text-theme-text text-sm">{counts}</p>}
      {project.error && <p className="text-theme-danger-text text-xs">{project.error}</p>}
      {project.proxied ? (
        <a
          href={hubProjectHref(project.name)}
          className={buttonClass('primary', 'lg', 'mt-1 self-start', true)}
        >
          Open dashboard
        </a>
      ) : (
        <p className="text-theme-warning-text text-xs" data-testid="hub-not-proxied">
          Not reachable through the hub: {project.proxyError ?? 'unknown reason'}. Open{' '}
          <span className="font-mono">{project.url}</span> directly.
        </p>
      )}
    </Card>
  );
}
