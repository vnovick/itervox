import { useHubProjects } from '../../queries/hub';
import { HubProjectCard } from '../../components/itervox/HubProjectCard';
import { EmptyState } from '../../components/ui/EmptyState';
import { ItervoxLogo } from '../../components/brand/ItervoxLogo';

// #228 — the hub's page at /hub/: every project on this machine, one URL.
// Each card opens that project's unchanged dashboard at /hub/p/{name}/, whose
// API the hub proxies with this hub token's scope.
export default function HubPage() {
  const { data, isLoading, error } = useHubProjects();
  const projects = data?.projects ?? [];
  return (
    <main className="mx-auto flex min-h-screen max-w-5xl flex-col gap-4 p-4 md:p-8">
      <header className="flex flex-wrap items-center gap-3">
        <ItervoxLogo className="h-6 w-auto" aria-label="Itervox" />
        <h1 className="text-theme-text text-lg font-semibold">Hub</h1>
        {data && (
          <span className="text-theme-muted text-xs">
            {projects.length} project{projects.length === 1 ? '' : 's'} · itervox {data.hub.version}
          </span>
        )}
      </header>
      {isLoading && <p className="text-theme-text-secondary text-sm">Loading projects…</p>}
      {error && (
        <p role="alert" className="text-theme-danger-text text-sm">
          Could not load the hub's projects: {error.message}
        </p>
      )}
      {data && projects.length === 0 && (
        <EmptyState
          title="No projects yet"
          description="Start itervox in a project directory; it joins this hub on its own."
        />
      )}
      <ul className="grid grid-cols-1 gap-3 md:grid-cols-2" aria-label="Projects">
        {projects.map((p) => (
          <li key={p.workflow}>
            <HubProjectCard project={p} />
          </li>
        ))}
      </ul>
    </main>
  );
}
