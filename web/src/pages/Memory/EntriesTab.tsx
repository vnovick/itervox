// Memory page, Entries tab (#202): add, filter and list entries; the
// selected one opens in the detail panel.

import { useMemo } from 'react';
import { EmptyState } from '../../components/ui/EmptyState';
import { useUIStore } from '../../store/uiStore';
import type { MemoryView } from '../../types/schemas';
import { MemoryDetailPanel } from './MemoryDetailPanel';
import { MemoryFilterBar } from './MemoryFilterBar';
import { MemoryList } from './MemoryList';
import { MemoryNewEntry } from './MemoryNewEntry';
import { MemoryUndoBanner } from './MemoryUndoBanner';
import { distinctValues, filterEntries } from './memoryModel';

export function EntriesTab({ view }: { view: MemoryView }) {
  const filters = useUIStore((s) => s.memoryFilters);
  const selectedId = useUIStore((s) => s.memorySelectedId);
  const shown = useMemo(() => filterEntries(view.entries, filters), [view.entries, filters]);
  const tags = useMemo(() => distinctValues(view.entries, 'tags'), [view.entries]);
  const profiles = useMemo(() => distinctValues(view.entries, 'profiles'), [view.entries]);
  const selected = view.entries.find((e) => e.id === selectedId);
  const brokenFiles = view.files.filter((f) => f.error);

  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,26rem)]">
      <section className="min-w-0 space-y-4" aria-label="Memory entries">
        {brokenFiles.map((f) => (
          <p key={f.scope} role="alert" className="text-theme-danger-text text-sm">
            {f.path}: {f.error} — its entries are not used until it is fixed.
          </p>
        ))}
        <MemoryUndoBanner />
        <MemoryNewEntry files={view.files} />
        <MemoryFilterBar tags={tags} profiles={profiles} />
        {shown.length === 0 ? (
          <EmptyState
            data-testid="memory-empty"
            title={view.entries.length === 0 ? 'No memory yet' : 'No entry matches the filters'}
            description={
              view.entries.length === 0
                ? 'Add what you would otherwise tell every agent again, like “this repo uses pnpm”.'
                : undefined
            }
          />
        ) : (
          <MemoryList entries={shown} selectedId={selectedId} />
        )}
      </section>
      {selected ? (
        <MemoryDetailPanel key={selected.id} entry={selected} />
      ) : (
        <aside className="text-theme-muted text-sm">Select an entry to see its details.</aside>
      )}
    </div>
  );
}
