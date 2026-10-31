// Memory page entry list (#202): one row per entry; selecting a row opens
// it in the detail panel.

import { useUIStore } from '../../store/uiStore';
import type { MemoryEntry } from '../../types/schemas';
import { SCOPE_INFO, TYPE_LABELS } from './memoryModel';

export function MemoryList({
  entries,
  selectedId,
}: {
  entries: MemoryEntry[];
  selectedId: string | null;
}) {
  const select = useUIStore((s) => s.setMemorySelectedId);
  return (
    <ul className="space-y-2" data-testid="memory-list">
      {entries.map((e) => {
        const active = e.id === selectedId;
        return (
          <li key={`${e.scope}-${e.id}`}>
            <button
              type="button"
              aria-pressed={active}
              className="choice-card w-full flex-col items-start gap-1.5 rounded-[var(--radius-md)] px-3.5 py-2.5 text-sm"
              onClick={() => {
                select(active ? null : e.id);
              }}
            >
              <span className="text-theme-text block w-full truncate">
                {e.pinned && (
                  <span className="text-theme-accent-strong mr-1.5 text-xs font-semibold">
                    Pinned
                  </span>
                )}
                {e.text}
              </span>
              <span
                className="flex flex-wrap justify-start gap-1.5"
                data-testid="memory-entry-meta"
              >
                <span className="tag">{TYPE_LABELS[e.type]}</span>
                <span className="tag">{SCOPE_INFO[e.scope].label}</span>
                {e.status === 'archived' && <span className="tag pill-warn">Archived</span>}
                <span className="tag">
                  {e.useCount === 0
                    ? 'never used'
                    : `used by ${String(e.useCount)} run${e.useCount === 1 ? '' : 's'}`}
                </span>
                {e.tags.map((t) => (
                  <span key={t} className="tag">
                    #{t}
                  </span>
                ))}
              </span>
            </button>
          </li>
        );
      })}
    </ul>
  );
}
