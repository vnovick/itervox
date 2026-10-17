import { useState, useEffect, useRef, useMemo } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useProjects } from '../../queries/projects';
import { useTrackerLabels, useTrackerUsers } from '../../queries/trackerScope';
import { useItervoxStore } from '../../store/itervoxStore';
import type { TrackerScopeInput } from '../../hooks/useSettingsActions';

interface Props {
  activeFilter: string[] | undefined;
  onSetFilter: (slugs: string[] | null) => Promise<boolean>;
  /** #182 — saves the combined scope; used when the tracker supports it. */
  onSetScope?: (scope: TrackerScopeInput) => Promise<boolean>;
}

const EMPTY: string[] = [];

/**
 * The tracker scope filter (#182): projects (or all) AND an assignee (anyone,
 * me, or a user) AND labels (any of them). The three controls are independent
 * and combine; trackers without a combinable scope show the projects only.
 */
export function ProjectFilterCard({ activeFilter, onSetFilter, onSetScope }: Props) {
  const { data: projects = [], isLoading, isError } = useProjects();
  const { scopeSupported, scopeAssignee, scopeLabels } = useItervoxStore(
    useShallow((s) => ({
      scopeSupported: Boolean(s.snapshot?.trackerScope),
      scopeAssignee: s.snapshot?.scopeAssignee ?? '',
      scopeLabels: s.snapshot?.scopeLabels ?? EMPTY,
    })),
  );
  const setScope = scopeSupported ? onSetScope : undefined;
  const combined = setScope !== undefined;
  const { data: labels = EMPTY } = useTrackerLabels(combined);
  const { data: users = [] } = useTrackerUsers(combined);
  const isDefaultMode = activeFilter === undefined;

  // Derive the server-side set for comparison without triggering effects.
  const serverSet = useMemo(
    () => new Set(activeFilter ?? []),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [activeFilter?.join('\0')],
  );

  const [selected, setSelected] = useState<Set<string>>(() => serverSet);
  const [assignee, setAssignee] = useState(scopeAssignee);
  const [selectedLabels, setSelectedLabels] = useState<Set<string>>(() => new Set(scopeLabels));
  const [saving, setSaving] = useState(false);

  // Track which server values we last synced to so we only overwrite local
  // edits when the server value actually changes, not on every render.
  const prevServerRef = useRef(serverSet);
  useEffect(() => {
    if (prevServerRef.current !== serverSet) {
      prevServerRef.current = serverSet;
      setSelected(serverSet);
    }
  }, [serverSet]);
  useEffect(() => {
    setAssignee(scopeAssignee);
  }, [scopeAssignee]);
  const labelsKey = scopeLabels.join('\0');
  useEffect(() => {
    setSelectedLabels(new Set(labelsKey ? labelsKey.split('\0') : []));
  }, [labelsKey]);

  const toggle = (slug: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(slug)) next.delete(slug);
      else next.add(slug);
      return next;
    });
  };
  const toggleLabel = (label: string) => {
    setSelectedLabels((prev) => {
      const next = new Set(prev);
      if (next.has(label)) next.delete(label);
      else next.add(label);
      return next;
    });
  };

  const handleSave = async () => {
    setSaving(true);
    if (setScope) {
      await setScope({ projects: [...selected], assignee, labels: [...selectedLabels] });
    } else {
      await onSetFilter([...selected]);
    }
    setSaving(false);
  };

  const handleReset = async () => {
    setSaving(true);
    await onSetFilter(null);
    setSaving(false);
  };

  const selectAll = () => {
    setSelected(new Set());
  };
  const allSelected = selected.size === 0;

  // The current assignee may be a user outside the list (an email, or one
  // the key lists no more): keep it selectable.
  const assigneeKnown =
    assignee === '' || assignee === 'me' || users.some((u) => u.id === assignee);
  const labelChoices = useMemo(
    () => [...new Set([...labels, ...scopeLabels])].sort((a, b) => a.localeCompare(b)),
    [labels, scopeLabels],
  );

  return (
    <div className="border-theme-line bg-theme-bg-elevated overflow-hidden rounded-[var(--radius-md)] border">
      <div className="border-theme-line bg-theme-panel-strong border-b px-5 py-4">
        <h2 className="text-theme-text text-sm font-semibold">
          {combined ? 'Tracker Scope' : 'Project Filter'}
        </h2>
        <p className="text-theme-text-secondary mt-0.5 text-xs">
          {combined
            ? 'Itervox reads the issues that match all three: the projects, the assignee and any of the labels. Leave a control empty to not filter by it.'
            : 'Limit Itervox to specific Linear projects. Leave all unchecked to include every project.'}
        </p>
      </div>

      <div className="space-y-4 px-5 py-5">
        {isLoading && <p className="text-theme-muted text-sm">Loading projects…</p>}

        {isError && (
          <p className="text-theme-danger-text text-sm">
            Failed to load projects. Check that the server is running.
          </p>
        )}

        {!isLoading && !isError && (
          <>
            <fieldset className="space-y-2">
              {combined && (
                <legend className="text-theme-muted mb-1 text-[11px] font-semibold tracking-widest uppercase">
                  Projects
                </legend>
              )}
              <label className="flex cursor-pointer items-center gap-2.5">
                <input
                  type="checkbox"
                  checked={allSelected}
                  onChange={selectAll}
                  className="h-4 w-4 rounded"
                  style={{ accentColor: 'var(--accent)' }}
                />
                <span className="text-theme-text text-sm font-medium">All projects</span>
              </label>
              {projects.map((p) => (
                <label key={p.slug} className="flex cursor-pointer items-center gap-2.5 pl-1">
                  <input
                    type="checkbox"
                    checked={selected.has(p.slug)}
                    onChange={() => {
                      toggle(p.slug);
                    }}
                    className="h-4 w-4 rounded"
                    style={{ accentColor: 'var(--accent)' }}
                  />
                  <span className="text-theme-text text-sm">{p.name}</span>
                  <span className="text-theme-muted font-mono text-xs">{p.slug}</span>
                </label>
              ))}
            </fieldset>

            {combined && (
              <label className="block space-y-1">
                <span className="text-theme-muted text-[11px] font-semibold tracking-widest uppercase">
                  Assignee
                </span>
                <select
                  aria-label="Assignee"
                  value={assignee}
                  onChange={(e) => {
                    setAssignee(e.target.value);
                  }}
                  className="border-theme-line bg-theme-bg text-theme-text block w-full rounded-[var(--radius-sm)] border px-2 py-1.5 text-sm"
                >
                  <option value="">Anyone</option>
                  <option value="me">Me (the API key&apos;s user)</option>
                  {!assigneeKnown && <option value={assignee}>{assignee}</option>}
                  {users.map((u) => (
                    <option key={u.id} value={u.id}>
                      {u.name || u.email || u.id}
                    </option>
                  ))}
                </select>
              </label>
            )}

            {combined && (
              <fieldset className="space-y-2">
                <legend className="text-theme-muted mb-1 text-[11px] font-semibold tracking-widest uppercase">
                  Labels (any of)
                </legend>
                {labelChoices.length === 0 && (
                  <p className="text-theme-muted text-xs">No labels found.</p>
                )}
                <div className="flex flex-wrap gap-x-4 gap-y-2">
                  {labelChoices.map((l) => (
                    <label key={l} className="flex cursor-pointer items-center gap-2">
                      <input
                        type="checkbox"
                        checked={selectedLabels.has(l)}
                        onChange={() => {
                          toggleLabel(l);
                        }}
                        className="h-4 w-4 rounded"
                        style={{ accentColor: 'var(--accent)' }}
                      />
                      <span className="text-theme-text text-sm">{l}</span>
                    </label>
                  ))}
                </div>
              </fieldset>
            )}

            {isDefaultMode && (
              <p className="text-theme-muted text-xs">
                Currently using the WORKFLOW.md default project slug.
              </p>
            )}

            <div className="flex items-center gap-2 pt-1">
              <button
                onClick={handleSave}
                disabled={saving}
                className="bg-theme-accent rounded-[var(--radius-sm)] px-4 py-2 text-sm font-medium text-white transition-colors disabled:opacity-50"
              >
                {saving ? 'Saving…' : 'Save filter'}
              </button>
              {!isDefaultMode && (
                <button
                  onClick={handleReset}
                  disabled={saving}
                  className="border-theme-line text-theme-text-secondary rounded-[var(--radius-sm)] border px-4 py-2 text-sm font-medium transition-colors hover:opacity-80 disabled:opacity-50"
                >
                  Reset to default
                </button>
              )}
            </div>
          </>
        )}
      </div>
    </div>
  );
}
