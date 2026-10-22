import Badge from '../ui/badge/Badge';
import { Card } from '../ui/Card/Card';
import { hubProjectHref } from '../../queries/hub';
import type { HubProject } from '../../types/schemas';

// #228 — one hub member on the hub's project switcher.

const HEALTH_COLOR: Record<string, 'success' | 'warning' | 'error'> = {
  ok: 'success',
  unauthorized: 'warning',
};

function countsLine(p: HubProject): string {
  const c = p.summary?.counts;
  if (!c) return '';
  const parts = [`${String(c.running ?? 0)} running`];
  if (c.inputRequired) parts.push(`${String(c.inputRequired)} need input`);
  if (c.retrying) parts.push(`${String(c.retrying)} retrying`);
  if (p.summary?.paused) parts.push('dispatch paused');
  return parts.join(' · ');
}

export function HubProjectCard({ project }: { project: HubProject }) {
  const counts = countsLine(project);
  return (
    <Card padding="md" className="flex flex-col gap-2">
      <div className="flex min-w-0 items-center gap-2">
        <span className="text-theme-text truncate font-mono text-sm font-semibold">
          {project.name}
        </span>
        <Badge size="sm" color={HEALTH_COLOR[project.health] ?? 'error'}>
          {project.health}
        </Badge>
        <span className="text-theme-muted ml-auto shrink-0 text-xs">{project.version}</span>
      </div>
      <p className="text-theme-text-secondary truncate text-xs" title={project.projectDir}>
        {project.projectDir}
      </p>
      {counts && <p className="text-theme-text text-xs">{counts}</p>}
      {project.error && <p className="text-theme-danger-text text-xs">{project.error}</p>}
      {project.proxied ? (
        <a
          href={hubProjectHref(project.name)}
          className="text-theme-accent-strong self-start text-sm font-medium hover:underline"
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
