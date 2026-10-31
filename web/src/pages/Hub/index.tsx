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
    <main className="mx-auto flex min-h-screen max-w-[1180px] flex-col gap-4 p-4 md:px-8 md:py-6">
      <header className="glass-panel flex flex-wrap items-center gap-3 px-5 py-4">
        <span aria-hidden="true" className="brand-tile h-9 w-9 rounded-[10px]" />
        <div className="min-w-0">
          <h1 className="text-theme-text text-2xl leading-tight font-bold tracking-tight">Hub</h1>
          {data && (
            <p className="text-theme-text-secondary text-[13px]">
              {projects.length} project{projects.length === 1 ? '' : 's'} · itervox{' '}
              {data.hub.version}
            </p>
          )}
        </div>
        <ItervoxLogo className="sr-only" aria-label="Itervox" />
        <div role="tablist" aria-label="Hub views" className="segmented ml-auto">
          {VIEWS.map((v) => (
            <button
              key={v.id}
              type="button"
              role="tab"
              aria-selected={view === v.id}
              onClick={() => {
                setView(v.id);
              }}
              className="text-sm"
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
        <ul className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3" aria-label="Projects">
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
