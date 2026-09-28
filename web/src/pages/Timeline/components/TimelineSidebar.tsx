import { ConfirmModal } from '../../../components/ui/modal';
import { Select } from '../../../components/ui/Select';
import { dotStyle } from '../../../components/itervox/timeline/types';
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
        className="border-theme-line bg-theme-panel hidden w-[180px] flex-shrink-0 flex-col border-r md:flex"
      >
        <div className="border-theme-line border-b px-3 py-3">
          <p className="text-theme-muted text-[10px] font-bold tracking-[0.08em] uppercase">
            Issues
          </p>
        </div>

        <div
          className="flex-1 overflow-y-auto p-2"
          style={{ display: 'flex', flexDirection: 'column', gap: 8 }}
        >
          {issueGroups.length === 0 ? (
            <p className="text-theme-muted px-2 py-4 text-xs italic">No sessions yet</p>
          ) : (
            issueGroups.map((group) => {
              const isSelected = selectedId === group.identifier;
              const isLive = group.latestStatus === 'live';
              return (
                <div key={group.identifier} className="group relative">
                  <button
                    onClick={() => {
                      onSelectIssue(group.identifier);
                    }}
                    className="w-full text-left transition-all"
                    style={{
                      display: 'flex',
                      alignItems: 'center',
                      gap: 10,
                      padding: '11px 12px',
                      borderRadius: 'var(--radius-md)',
                      border: `1px solid ${isSelected ? 'var(--accent)' : 'var(--line)'}`,
                      background: isSelected ? 'var(--accent-soft)' : 'var(--bg-elevated)',
                      cursor: 'pointer',
                      font: 'inherit',
                    }}
                  >
                    <span
                      className={`flex-shrink-0 rounded-full ${isLive ? 'animate-pulse' : ''}`}
                      style={{ width: 8, height: 8, ...dotStyle(group.latestStatus) }}
                    />
                    <div className="min-w-0 pr-4">
                      <div
                        className="text-theme-text truncate font-mono font-semibold"
                        style={{ fontSize: 14 }}
                      >
                        {group.identifier}
                      </div>
                      <div
                        className="text-theme-text-secondary"
                        style={{ fontSize: 12, marginTop: 2 }}
                      >
                        {statusLabel(group)}
                      </div>
                    </div>
                  </button>
                  {!isLive && (
                    <button
                      onClick={(e) => {
                        e.stopPropagation();
                        onRequestClear(group.identifier);
                      }}
                      title="Clear all session logs"
                      className="absolute top-1/2 right-2 -translate-y-1/2 opacity-0 transition-opacity group-hover:opacity-100"
                      style={{
                        background: 'transparent',
                        border: 'none',
                        cursor: 'pointer',
                        padding: 4,
                        color: 'var(--muted)',
                        lineHeight: 1,
                      }}
                    >
                      <svg
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
      <div className="bg-theme-panel border-theme-line flex flex-shrink-0 items-center gap-2 border-b px-3 py-2 md:hidden">
        <label htmlFor="timeline-issue-select" className="text-theme-text-secondary text-[11px]">
          Issue
        </label>
        <Select
          id="timeline-issue-select"
          data-testid="timeline-issue-select"
          value={selectedId ?? ''}
          onChange={(event) => {
            onSelectIssue(event.target.value);
          }}
          className="h-8 min-w-0 flex-1 text-xs"
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
