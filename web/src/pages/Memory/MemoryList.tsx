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
    <ul
      className="divide-theme-line border-theme-line divide-y rounded border"
      data-testid="memory-list"
    >
      {entries.map((e) => {
        const active = e.id === selectedId;
        return (
          <li key={`${e.scope}-${e.id}`}>
            <button
              type="button"
              aria-pressed={active}
              className={`w-full px-3 py-2 text-left text-sm ${active ? 'bg-theme-accent-soft' : 'hover:bg-theme-panel-strong'}`}
              onClick={() => {
                select(active ? null : e.id);
              }}
            >
              <span className="text-theme-text block truncate">
                {e.pinned && (
                  <span className="text-theme-accent-strong mr-1.5 text-xs font-semibold">
                    Pinned
                  </span>
                )}
                {e.text}
              </span>
              <span className="text-theme-muted mt-0.5 flex flex-wrap gap-x-3 text-xs">
                <span>{TYPE_LABELS[e.type]}</span>
                <span>{SCOPE_INFO[e.scope].label}</span>
                {e.status === 'archived' && <span>Archived</span>}
                <span>
                  {e.useCount === 0
                    ? 'never used'
                    : `used by ${String(e.useCount)} run${e.useCount === 1 ? '' : 's'}`}
                </span>
                {e.tags.length > 0 && <span>#{e.tags.join(' #')}</span>}
              </span>
            </button>
          </li>
        );
      })}
    </ul>
  );
}
