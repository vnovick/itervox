import { useShallow } from 'zustand/react/shallow';
import { useItervoxStore } from '../../store/itervoxStore';
import { EMPTY_STATES as EMPTY_PROJECT_FILTER } from '../../utils/constants';

const LinearIcon = () => (
  <svg width="14" height="14" viewBox="0 0 12 12" fill="none" aria-hidden="true">
    <rect x="0.5" y="0.5" width="4.5" height="4.5" rx="1" fill="currentColor" />
    <rect x="7" y="0.5" width="4.5" height="4.5" rx="1" fill="currentColor" />
    <rect x="0.5" y="7" width="4.5" height="4.5" rx="1" fill="currentColor" />
    <rect x="7" y="7" width="4.5" height="4.5" rx="1" fill="currentColor" />
  </svg>
);

const GitHubIcon = () => (
  <svg width="14" height="14" viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
    <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.013 8.013 0 0016 8c0-4.42-3.58-8-8-8z" />
  </svg>
);

const FileIcon = () => (
  <svg width="14" height="14" viewBox="0 0 16 16" fill="none" aria-hidden="true">
    <path
      d="M3.5 1.5h6l3 3v10h-9z M9.5 1.5v3h3"
      stroke="currentColor"
      strokeWidth="1.2"
      strokeLinejoin="round"
    />
  </svg>
);

// Unknown kinds (a newer daemon) show their raw name.
const TRACKER_LABEL: Partial<Record<string, string>> = {
  linear: 'Linear',
  github: 'GitHub',
  local: 'Local files',
  memory: 'Demo',
};

function TrackerIcon({ kind }: { kind: string }) {
  if (kind === 'linear') return <LinearIcon />;
  if (kind === 'github') return <GitHubIcon />;
  return <FileIcon />;
}

export function ProjectSelector() {
  const { trackerKind, activeProjectFilter } = useItervoxStore(
    useShallow((s) => ({
      trackerKind: s.snapshot?.trackerKind,
      activeProjectFilter: s.snapshot?.activeProjectFilter ?? EMPTY_PROJECT_FILTER,
    })),
  );

  if (!trackerKind) return null;

  const showGitHubNotice = trackerKind === 'github' && activeProjectFilter.length === 0;

  return (
    <div className="flex flex-col gap-2">
      <div
        data-testid="project-selector"
        className="bg-theme-panel border-theme-line flex items-center gap-3 rounded-[var(--radius-md)] border px-4 py-2.5"
      >
        {/* SOURCE label */}
        <span className="flex-shrink-0 text-[10px] font-semibold tracking-widest uppercase">
          Source
        </span>

        {/* Active tracker — read-only badge, not a switcher */}
        <span
          data-testid="tracker-badge"
          className="inline-flex items-center gap-1.5 rounded-[6px] px-2.5 py-1 text-[12px] font-medium"
          style={{
            background: 'var(--bg-elevated)',
            color: 'var(--text)',
            border: '1px solid var(--line)',
          }}
        >
          <TrackerIcon kind={trackerKind} />
          {TRACKER_LABEL[trackerKind] ?? trackerKind}
        </span>

        {/* Active project filter chips — only shown when a filter is configured */}
        {activeProjectFilter.length > 0 && (
          <div className="flex flex-wrap gap-1.5">
            {activeProjectFilter.map((project) => (
              <span
                key={project}
                className="bg-theme-accent-soft text-theme-accent-strong rounded-full px-2.5 py-0.5 text-xs font-medium"
              >
                {project}
              </span>
            ))}
          </div>
        )}
      </div>

      {/* GitHub notice — no repositories selected means all repos are tracked */}
      {showGitHubNotice && (
        <div
          className="flex items-start gap-2.5 rounded-[var(--radius-md)] px-4 py-3 text-xs"
          style={{
            background: 'rgba(245,158,11,0.08)',
            border: '1px solid rgba(245,158,11,0.2)',
            color: 'var(--warning-text)',
          }}
        >
          <span className="mt-px flex-shrink-0">⚠</span>
          <span>
            No GitHub projects are selected — all repositories will be tracked. To filter to
            specific repos, add them under{' '}
            <strong className="text-theme-text">Settings → Project Filter</strong>.
          </span>
        </div>
      )}
    </div>
  );
}
