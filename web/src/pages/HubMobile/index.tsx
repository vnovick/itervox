import { useState } from 'react';
import { useHubProjects } from '../../queries/hub';
import { useHubAttention } from '../../queries/hubPhone';
import { HubProjectCard } from '../../components/itervox/HubProjectCard';
import { NeedsAttentionList } from '../../components/itervox/hubPhone/NeedsAttentionList';
import { PushDevicePanel } from '../../components/itervox/hubPhone/PushDevicePanel';
import { ItervoxLogo } from '../../components/brand/ItervoxLogo';
import type { HubProject } from '../../types/schemas';

// #231 — the hub on a phone (/hub/m, the installed web app's start page):
// what needs you across every project, the projects, and this device's push
// notifications. An item opens the issue's mobile page in its project
// (/hub/p/{project}/m/{issue}): Now, reply, approve or reject, steer, pause.

type MobileView = 'attention' | 'projects' | 'device';
const VIEWS: readonly { id: MobileView; label: string }[] = [
  { id: 'attention', label: 'Needs you' },
  { id: 'projects', label: 'Projects' },
  { id: 'device', label: 'This phone' },
];
const EMPTY_PROJECTS: readonly HubProject[] = [];

export default function HubMobilePage() {
  const { data, error } = useHubProjects();
  const projects = data?.projects ?? EMPTY_PROJECTS;
  const attention = useHubAttention(projects);
  const [view, setView] = useState<MobileView>('attention');

  return (
    <main className="mx-auto flex min-h-screen max-w-xl flex-col gap-3 px-4 pt-[max(1rem,env(safe-area-inset-top))] pb-24">
      <header className="flex items-center gap-2">
        <ItervoxLogo className="h-5 w-auto" />
        <h1 className="text-theme-text text-base font-semibold">Hub</h1>
        <a href="/hub/" className="text-theme-muted ml-auto text-xs underline">
          Full dashboard
        </a>
      </header>
      {error && (
        <p role="alert" className="text-theme-danger-text text-sm">
          Could not reach the hub: {error.message}
        </p>
      )}
      {view === 'attention' && (
        <NeedsAttentionList
          items={attention.items}
          loading={attention.isLoading || !data}
          failedProjects={attention.failedProjects}
        />
      )}
      {view === 'projects' && (
        <ul className="flex flex-col gap-2" aria-label="Projects">
          {projects.map((p) => (
            <li key={p.workflow}>
              <HubProjectCard project={p} />
            </li>
          ))}
        </ul>
      )}
      {view === 'device' && <PushDevicePanel />}
      <div
        role="tablist"
        aria-label="Hub views"
        className="border-theme-line bg-theme-bg fixed inset-x-0 bottom-0 flex border-t pb-[env(safe-area-inset-bottom)]"
      >
        {VIEWS.map((v) => (
          <button
            key={v.id}
            type="button"
            role="tab"
            aria-selected={view === v.id}
            onClick={() => {
              setView(v.id);
            }}
            className={`h-14 flex-1 text-sm ${view === v.id ? 'text-theme-accent-strong font-semibold' : 'text-theme-muted'}`}
          >
            {v.label}
            {v.id === 'attention' && attention.items.length > 0 && (
              <span className="bg-theme-accent ml-1 rounded-full px-1.5 text-xs text-white">
                {attention.items.length}
              </span>
            )}
          </button>
        ))}
      </div>
    </main>
  );
}
