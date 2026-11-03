import { useState } from 'react';
import { useHubProjects } from '../../queries/hub';
import { HubNotificationSettings } from '../../components/itervox/HubNotificationSettings';
import { HubProjectCard } from '../../components/itervox/HubProjectCard';
import { HubPairingPanel } from '../../components/itervox/hubPhone/HubPairingPanel';
import { EmptyState } from '../../components/ui/EmptyState';
import { ItervoxLogo } from '../../components/brand/ItervoxLogo';

// #228 — the hub's page at /hub/: every project on this machine, one URL.
// Each card opens that project's unchanged dashboard at /hub/p/{name}/, whose
// API the hub proxies with this hub token's scope. #229 adds the
// Notifications settings view (admin) beside the project list; #231 the
// Phones view (pairing QR code, paired devices, push devices).
type HubView = 'projects' | 'notifications' | 'phones';
const VIEWS: readonly { id: HubView; label: string }[] = [
  { id: 'projects', label: 'Projects' },
  { id: 'notifications', label: 'Notifications' },
  { id: 'phones', label: 'Phones' },
];

// #232 — the Mac app's tray opens a view directly: /hub/?view=phones
// ("Phone…", the pairing QR code) and /hub/?view=notifications.
function initialView(): HubView {
  const wanted = new URLSearchParams(window.location.search).get('view');
  return VIEWS.find((v) => v.id === wanted)?.id ?? 'projects';
}

export default function HubPage() {
  const { data, isLoading, error } = useHubProjects();
  const [view, setView] = useState<HubView>(initialView);
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
        <div role="tablist" aria-label="Hub views" className="ml-auto flex gap-1">
          {VIEWS.map((v) => (
            <button
              key={v.id}
              type="button"
              role="tab"
              aria-selected={view === v.id}
              onClick={() => {
                setView(v.id);
              }}
              className={
                view === v.id
                  ? 'bg-theme-accent-soft text-theme-accent-strong rounded px-2 py-1 text-xs font-medium'
                  : 'text-theme-muted hover:text-theme-text rounded px-2 py-1 text-xs'
              }
            >
              {v.label}
            </button>
          ))}
        </div>
      </header>
      {view === 'notifications' ? <HubNotificationSettings /> : null}
      {view === 'phones' ? <HubPairingPanel /> : null}
      {view === 'projects' && isLoading && (
        <p className="text-theme-text-secondary text-sm">Loading projects…</p>
      )}
      {view === 'projects' && error && (
        <p role="alert" className="text-theme-danger-text text-sm">
          Could not load the hub's projects: {error.message}
        </p>
      )}
      {view === 'projects' && data && projects.length === 0 && (
        <EmptyState
          title="No projects yet"
          description="Start itervox in a project directory; it joins this hub on its own."
        />
      )}
      {view === 'projects' && (
        <ul className="grid grid-cols-1 gap-3 md:grid-cols-2" aria-label="Projects">
          {projects.map((p) => (
            <li key={p.workflow}>
              <HubProjectCard project={p} />
            </li>
          ))}
        </ul>
      )}
    </main>
  );
}
