import { hubProjectHref, useHubProjects } from '../../queries/hub';
import { Select } from '../ui/Select';

// #228 — the project select of HubProjectSwitcher, in its own lazy chunk so
// a daemon's own dashboard never loads it (the bundle budget).
export default function HubProjectSelect({ current }: { current: string }) {
  const { data } = useHubProjects();
  const projects = data?.projects ?? [];
  return (
    <label className="flex shrink-0 items-center gap-1 text-xs">
      <span className="sr-only">Project</span>
      <Select
        aria-label="Switch project"
        className="h-9 w-auto min-w-[150px] rounded-full py-0 pl-3.5 font-mono text-[13px]"
        value={current}
        onChange={(e) => {
          const next = e.target.value;
          if (next !== current)
            window.location.assign(next === '' ? '/hub/' : hubProjectHref(next));
        }}
      >
        <option value="">All projects (hub)</option>
        {!projects.some((p) => p.name === current) && <option value={current}>{current}</option>}
        {projects.map((p) => (
          <option key={p.workflow} value={p.name} disabled={!p.proxied}>
            {p.name}
          </option>
        ))}
      </Select>
    </label>
  );
}
