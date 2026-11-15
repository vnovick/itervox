import { useState, type ReactNode } from 'react';
import { useHubProjects } from '../../queries/hub';
import { useHubAttention } from '../../queries/hubPhone';
import { HubProjectCard } from '../../components/itervox/HubProjectCard';
import { NeedsAttentionList } from '../../components/itervox/hubPhone/NeedsAttentionList';
import { PushDevicePanel } from '../../components/itervox/hubPhone/PushDevicePanel';
import { ItervoxLogo } from '../../components/brand/ItervoxLogo';
import { buttonClass } from '../../components/ui/button/buttonStyles';
import type { HubProject } from '../../types/schemas';

// #231 — the hub on a phone (/hub/m, the installed web app's start page):
// what needs you across every project, the projects, and this device's push
// notifications. An item opens the issue's mobile page in its project
// (/hub/p/{project}/m/{issue}): Now, reply, approve or reject, steer, pause.

type MobileView = 'attention' | 'projects' | 'device';
const ICON_PROPS = {
  width: 22,
  height: 22,
  viewBox: '0 0 24 24',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 1.8,
  strokeLinecap: 'round',
  strokeLinejoin: 'round',
  'aria-hidden': true,
} as const;
const VIEWS: readonly { id: MobileView; label: string; icon: ReactNode }[] = [
  {
    id: 'attention',
    label: 'Needs you',
    icon: (
      <svg {...ICON_PROPS}>
        <path d="M6 8a6 6 0 1 1 12 0c0 7 3 9 3 9H3s3-2 3-9" />
        <path d="M10.3 21a1.94 1.94 0 0 0 3.4 0" />
      </svg>
    ),
  },
  {
    id: 'projects',
    label: 'Projects',
    icon: (
      <svg {...ICON_PROPS}>
        <rect x="4" y="4" width="6.5" height="6.5" rx="1.5" />
        <rect x="13.5" y="4" width="6.5" height="6.5" rx="1.5" />
        <rect x="4" y="13.5" width="6.5" height="6.5" rx="1.5" />
        <rect x="13.5" y="13.5" width="6.5" height="6.5" rx="1.5" />
      </svg>
    ),
  },
  {
    id: 'device',
    label: 'This phone',
    icon: (
      <svg {...ICON_PROPS}>
        <rect x="7" y="2.5" width="10" height="19" rx="2.5" />
        <path d="M11 18h2" />
      </svg>
    ),
  },
];
const EMPTY_PROJECTS: readonly HubProject[] = [];

export default function HubMobilePage() {
  const { data, error } = useHubProjects();
  const projects = data?.projects ?? EMPTY_PROJECTS;
  const attention = useHubAttention(projects);
  const [view, setView] = useState<MobileView>('attention');

  return (
    <main className="mx-auto flex min-h-screen max-w-xl flex-col gap-3 px-4 pt-[max(2.5rem,env(safe-area-inset-top))] pb-32">
      <header className="flex items-center gap-2 pb-1">
        <ItervoxLogo className="sr-only" />
        <h1 className="text-theme-text text-[32px] leading-tight font-bold tracking-tight">Hub</h1>
        <a href="/hub/" className={buttonClass('secondary', 'md', 'ml-auto font-semibold', true)}>
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
        className="glass-panel fixed inset-x-4 bottom-[max(1rem,env(safe-area-inset-bottom))] mx-auto flex max-w-[calc(36rem-2rem)] gap-1 rounded-[26px] p-1.5"
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
            className={`flex min-h-14 flex-1 flex-col items-center justify-center gap-0.5 rounded-[20px] border text-xs ${
              view === v.id
                ? 'glass-card text-theme-accent-text font-semibold'
                : 'text-theme-text-secondary border-transparent'
            }`}
          >
            {v.icon}
            <span>
              {v.label}
              {v.id === 'attention' && attention.items.length > 0 && (
                <span className="bg-theme-accent ml-1 rounded-full px-1.5 text-white">
                  {attention.items.length}
                </span>
              )}
            </span>
          </button>
        ))}
      </div>
    </main>
  );
}
