import { ConfirmModal } from '../../../components/ui/modal';
import { Select } from '../../../components/ui/Select';
import { dotClass } from '../../../components/itervox/timeline/types';
import type { IssueGroup } from '../../../components/itervox/timeline/types';

interface TimelineSidebarProps {
  issueGroups: IssueGroup[];
  selectedId: string | null;
  onSelectIssue: (id: string) => void;
  confirmClearId: string | null;
  onRequestClear: (id: string) => void;
  onCancelClear: () => void;
  onConfirmClear: (id: string) => void;
  isClearPending: boolean;
}

function statusLabel(group: IssueGroup): string {
  const isLive = group.latestStatus === 'live';
  if (isLive) {
    return `${String(group.runs.length)} run${group.runs.length !== 1 ? 's' : ''} · live`;
  }
  if (group.latestStatus === 'failed') return 'failed + blocked';
  if (group.latestStatus === 'succeeded') return 'completed';
  return `${String(group.runs.length)} run${group.runs.length !== 1 ? 's' : ''}`;
}

export function TimelineSidebar({
  issueGroups,
  selectedId,
  onSelectIssue,
  confirmClearId,
  onRequestClear,
  onCancelClear,
  onConfirmClear,
  isClearPending,
}: TimelineSidebarProps) {
  return (
    <>
      <aside
        data-testid="timeline-sidebar"
        aria-label="Issues"
        className="glass-panel hidden max-h-full w-[250px] flex-shrink-0 flex-col gap-1.5 p-3 md:flex"
      >
        <h2 className="section-title px-1.5 pt-1 pb-1">Issues</h2>

        <div className="flex min-h-0 flex-col gap-0.5 overflow-y-auto">
          {issueGroups.length === 0 ? (
            <p className="text-theme-muted px-2 py-4 text-xs italic">No sessions yet</p>
          ) : (
            issueGroups.map((group) => {
              const isSelected = selectedId === group.identifier;
              const isLive = group.latestStatus === 'live';
              return (
                <div key={group.identifier} className="group relative">
                  <button
                    type="button"
                    aria-current={isSelected ? 'true' : undefined}
                    onClick={() => {
                      onSelectIssue(group.identifier);
                    }}
                    className="list-row min-h-11 pr-8"
                  >
                    <span
                      aria-hidden="true"
                      className={`status-dot h-[7px] w-[7px] ${dotClass(group.latestStatus)}`}
                    />
                    <span className="text-theme-text truncate font-mono text-xs font-semibold">
                      {group.identifier}
                    </span>
                    <span className="text-theme-text-secondary ml-auto truncate text-xs">
                      {statusLabel(group)}
                    </span>
                  </button>
                  {!isLive && (
                    <button
                      type="button"
                      onClick={(e) => {
                        e.stopPropagation();
                        onRequestClear(group.identifier);
                      }}
                      title="Clear all session logs"
                      aria-label={`Clear all session logs for ${group.identifier}`}
                      className="text-theme-muted hover:text-theme-danger-text absolute top-1/2 right-1.5 -translate-y-1/2 p-1 leading-none opacity-0 transition-opacity group-hover:opacity-100 focus-visible:opacity-100"
                    >
                      <svg
                        aria-hidden="true"
                        width="13"
                        height="13"
                        viewBox="0 0 16 16"
                        fill="none"
                        xmlns="http://www.w3.org/2000/svg"
                      >
                        <path
                          d="M2 4h12M5 4V2h6v2M6 7v5M10 7v5M3 4l1 10h8l1-10H3z"
                          stroke="currentColor"
                          strokeWidth="1.5"
                          strokeLinecap="round"
                          strokeLinejoin="round"
                        />
                      </svg>
                    </button>
                  )}
                </div>
              );
            })
          )}
        </div>
      </aside>
      {/* CORE-087 — below md the list collapses to a select above the detail pane
          (after the aside in DOM order, so desktop text lookups find the list first). */}
      <div className="glass-panel flex flex-shrink-0 items-center gap-2 px-3 py-2 md:hidden">
        <label htmlFor="timeline-issue-select" className="text-theme-text-secondary text-xs">
          Issue
        </label>
        <Select
          id="timeline-issue-select"
          data-testid="timeline-issue-select"
          value={selectedId ?? ''}
          onChange={(event) => {
            onSelectIssue(event.target.value);
          }}
          className="h-9 min-w-0 flex-1 text-xs"
        >
          {selectedId === null && <option value="">Select an issue</option>}
          {issueGroups.map((group) => (
            <option key={group.identifier} value={group.identifier}>
              {group.identifier} — {statusLabel(group)}
            </option>
          ))}
        </Select>
      </div>

      <ConfirmModal
        isOpen={!!confirmClearId}
        onClose={onCancelClear}
        onConfirm={() => {
          if (confirmClearId) onConfirmClear(confirmClearId);
        }}
        title={`Clear session logs for ${confirmClearId ?? ''}?`}
        description="All JSONL session files for this issue will be permanently deleted."
        confirmLabel="Clear all"
        pendingLabel="Clearing…"
        isPending={isClearPending}
        variant="danger"
      />
    </>
  );
}
