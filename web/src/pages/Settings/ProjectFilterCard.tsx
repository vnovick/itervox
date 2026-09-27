import { useState, useEffect, useRef, useMemo } from 'react';
import { useProjects } from '../../queries/projects';

interface Props {
  activeFilter: string[] | undefined;
  onSetFilter: (slugs: string[] | null) => Promise<boolean>;
}

export function ProjectFilterCard({ activeFilter, onSetFilter }: Props) {
  const { data: projects = [], isLoading, isError } = useProjects();
  const isDefaultMode = activeFilter === undefined;

  // Derive the server-side set for comparison without triggering effects.
  const serverSet = useMemo(
    () => new Set(activeFilter ?? []),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [activeFilter?.join('\0')],
  );

  const [selected, setSelected] = useState<Set<string>>(() => serverSet);
  const [saving, setSaving] = useState(false);

  // Track which serverSet identity we last synced to so we only overwrite
  // local edits when the server value actually changes, not on every render.
  const prevServerRef = useRef(serverSet);
  useEffect(() => {
    if (prevServerRef.current !== serverSet) {
      prevServerRef.current = serverSet;
      setSelected(serverSet);
    }
  }, [serverSet]);

  const toggle = (slug: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(slug)) next.delete(slug);
      else next.add(slug);
      return next;
    });
  };

  const handleSave = async () => {
    setSaving(true);
    await onSetFilter([...selected]);
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

  return (
    <div className="border-theme-line bg-theme-bg-elevated overflow-hidden rounded-[var(--radius-md)] border">
      <div className="border-theme-line bg-theme-panel-strong border-b px-5 py-4">
        <h2 className="text-theme-text text-sm font-semibold">Project Filter</h2>
        <p className="text-theme-text-secondary mt-0.5 text-xs">
          Limit Itervox to specific Linear projects. Leave all unchecked to include every project.
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
            <div className="space-y-2">
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
            </div>

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
