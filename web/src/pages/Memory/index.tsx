// Memory page (#202): what Itervox carries from one run to the next. The
// Entries tab lists private and project-shared entries with filters and a
// detail panel; the Lessons tab is the review queue for proposed lessons.

import type { KeyboardEvent } from 'react';
import PageMeta from '../../components/common/PageMeta';
import { Banner } from '../../components/ui/Banner';
import { useMemory } from '../../queries/memory';
import { useUIStore, type MemoryTab } from '../../store/uiStore';
import { EntriesTab } from './EntriesTab';
import { LessonsTab } from './LessonsTab';
import { proposedEntries } from './memoryModel';

const TABS: { id: MemoryTab; label: string }[] = [
  { id: 'entries', label: 'Entries' },
  { id: 'lessons', label: 'Lessons' },
];

export default function Memory() {
  const { data, isLoading, isError } = useMemory();
  const tab = useUIStore((s) => s.memoryTab);
  const setTab = useUIStore((s) => s.setMemoryTab);
  const proposed = data ? proposedEntries(data.entries).length : 0;

  const onKeyDown = (e: KeyboardEvent<HTMLButtonElement>, index: number) => {
    if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
    e.preventDefault();
    const next = TABS[(index + 1) % TABS.length];
    setTab(next.id);
    document.getElementById(`memory-tab-${next.id}`)?.focus();
  };

  return (
    <>
      <PageMeta
        title="Itervox | Memory"
        description="Itervox memory — what agents carry from one run to the next"
      />
      <div className="w-full max-w-6xl space-y-4">
        <div className="flex flex-wrap items-start gap-x-6 gap-y-3">
          <div className="min-w-0 flex-1 basis-[28rem]">
            <h1 className="page-title">Memory</h1>
            <p className="text-theme-text-secondary mt-1 max-w-3xl text-sm">
              Facts, preferences, decisions and lessons carried from one run to the next. Each run
              gets the pinned entries plus the ones that best match its issue and profile, within
              the memory budget, as a <code>## Memory</code> block after the rules and ahead of the
              profile’s SOUL.md. Secrets are redacted on save. Edits apply to the next run.
            </p>
          </div>
          <div role="tablist" aria-label="Memory views" className="segmented">
            {TABS.map((t, i) => {
              const active = t.id === tab;
              return (
                <button
                  key={t.id}
                  id={`memory-tab-${t.id}`}
                  type="button"
                  role="tab"
                  aria-selected={active}
                  aria-controls="memory-panel"
                  tabIndex={active ? 0 : -1}
                  className="text-[15px]"
                  onClick={() => {
                    setTab(t.id);
                  }}
                  onKeyDown={(e) => {
                    onKeyDown(e, i);
                  }}
                >
                  {t.label}
                  {t.id === 'lessons' && proposed > 0 && <span> · {proposed}</span>}
                </button>
              );
            })}
          </div>
        </div>
        {data && !data.enabled && (
          <Banner tone="warning">
            Memory is off (<code>memory.enabled: false</code>): runs get no memory block. You can
            still edit the entries here.
          </Banner>
        )}
        <div id="memory-panel" role="tabpanel" aria-labelledby={`memory-tab-${tab}`}>
          {isLoading && <p className="text-theme-muted text-sm">Loading memory…</p>}
          {isError && <p className="text-theme-danger-text text-sm">Could not load the memory.</p>}
          {data && tab === 'entries' && <EntriesTab view={data} />}
          {data && tab === 'lessons' && <LessonsTab view={data} />}
        </div>
      </div>
    </>
  );
}
