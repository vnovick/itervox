import { Fragment, useMemo, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { TRACKER_SCOPE_KEY, useTrackerScope } from '../../../queries/trackerScope';
import type { FilterGroup } from '../../../types/schemas';
import { cleanGroups, filterLabel, firstEmptyGroup } from '../../../utils/filterGroups';
import { FilterGroupEditor } from './FilterGroupEditor';
import { FilterPreviewLine } from './FilterPreviewLine';

// #198 — Settings → Tracker Scope: the tracker filter's group builder.
// Conditions inside a group AND; the groups OR. Saving writes tracker.filter
// to WORKFLOW.md (PUT /api/v1/tracker/scope with "filter", reload-fenced);
// the legacy project/assignee/labels keys are shown as one group and
// replaced by the filter on save.

interface Props {
  onSave: (filter: FilterGroup[]) => Promise<boolean>;
}

interface Row {
  id: number;
  group: FilterGroup;
}

let nextRowId = 0;
const toRows = (groups: FilterGroup[]): Row[] =>
  groups.map((group) => ({ id: ++nextRowId, group }));

export function TrackerFilterCard({ onSave }: Props) {
  const queryClient = useQueryClient();
  const { data: scope, isLoading, isError } = useTrackerScope();
  const serverKey = JSON.stringify(scope?.filter ?? []);
  const [rows, setRows] = useState<Row[]>(() => toRows(JSON.parse(serverKey) as FilterGroup[]));
  const [syncedKey, setSyncedKey] = useState(serverKey);
  const [saving, setSaving] = useState(false);

  // Re-sync the draft only when the server's filter actually changes
  // (adjusting state while rendering, not in an effect).
  if (syncedKey !== serverKey) {
    setSyncedKey(serverKey);
    setRows(toRows(JSON.parse(serverKey) as FilterGroup[]));
  }

  const groups = useMemo(() => rows.map((r) => r.group), [rows]);
  const cleaned = useMemo(() => cleanGroups(groups), [groups]);
  const emptyAt = firstEmptyGroup(groups);
  const dirty = JSON.stringify(cleaned) !== serverKey;
  const supported = scope?.conditions ?? [];

  const setGroup = (id: number, group: FilterGroup) => {
    setRows((prev) => prev.map((r) => (r.id === id ? { id, group } : r)));
  };
  const removeGroup = (id: number) => {
    setRows((prev) => prev.filter((r) => r.id !== id));
  };

  const handleSave = async () => {
    setSaving(true);
    const ok = await onSave(cleaned);
    if (ok) await queryClient.invalidateQueries({ queryKey: TRACKER_SCOPE_KEY });
    setSaving(false);
  };

  return (
    <div className="border-theme-line bg-theme-bg-elevated overflow-hidden rounded-[var(--radius-md)] border">
      <div className="border-theme-line bg-theme-panel-strong border-b px-5 py-4">
        <h2 className="text-theme-text text-sm font-semibold">Tracker Scope</h2>
        <p className="text-theme-text-secondary mt-0.5 text-xs">
          Itervox reads an issue when it matches <strong>any</strong> group (OR). Inside a group,{' '}
          <strong>every</strong> condition must match (AND). No groups: every issue.
        </p>
      </div>

      <div className="space-y-4 px-5 py-5">
        {isLoading && <p className="text-theme-muted text-sm">Loading the tracker scope…</p>}
        {isError && (
          <p className="text-theme-danger-text text-sm">
            Failed to load the tracker scope. Check that the server is running.
          </p>
        )}

        {scope && (
          <>
            {rows.length === 0 && (
              <p className="text-theme-muted text-sm">No filter — every issue is read.</p>
            )}
            {rows.map(({ id, group }, i) => (
              <Fragment key={id}>
                {i > 0 && (
                  <div className="flex items-center gap-2" aria-hidden="true">
                    <span className="bg-theme-line h-px flex-1" />
                    <span className="text-theme-accent-strong text-xs font-semibold tracking-widest">
                      OR
                    </span>
                    <span className="bg-theme-line h-px flex-1" />
                  </div>
                )}
                <FilterGroupEditor
                  group={group}
                  index={i}
                  supported={supported}
                  onChange={(g) => {
                    setGroup(id, g);
                  }}
                  onRemove={() => {
                    removeGroup(id);
                  }}
                />
              </Fragment>
            ))}

            <button
              type="button"
              onClick={() => {
                setRows((prev) => [...prev, ...toRows([{}])]);
              }}
              className="border-theme-line text-theme-text-secondary rounded-[var(--radius-sm)] border border-dashed px-3 py-1.5 text-xs hover:opacity-80"
            >
              + Add group (OR)
            </button>

            {cleaned.length > 0 && emptyAt < 0 && (
              <p className="text-theme-muted font-mono text-xs" data-testid="filter-summary">
                {filterLabel(cleaned)}
              </p>
            )}
            <FilterPreviewLine groups={cleaned} valid={emptyAt < 0} />

            <div className="flex items-center gap-2 pt-1">
              <button
                onClick={handleSave}
                disabled={saving || (!dirty && scope.filterMode) || emptyAt >= 0}
                className="bg-theme-accent rounded-[var(--radius-sm)] px-4 py-2 text-sm font-medium text-white transition-colors disabled:opacity-50"
              >
                {saving ? 'Saving…' : 'Save filter'}
              </button>
              {!scope.filterMode && scope.filter.length > 0 && (
                <span className="text-theme-muted text-xs">
                  Shown from the legacy project / assignee / labels keys; saving writes
                  tracker.filter.
                </span>
              )}
            </div>
          </>
        )}
      </div>
    </div>
  );
}
