// Memory page detail panel (#202): the full entry, its provenance and the
// runs that used it, with inline edit, pin, archive, delete (undo in the
// list) and promote (private → project shared, showing the git diff).

import { useState } from 'react';
import { Button } from '../../components/ui/button';
import { CopyButton } from '../../components/ui/CopyButton';
import {
  useDeleteMemory,
  usePinMemory,
  usePromoteMemory,
  useUpdateMemory,
} from '../../queries/memory';
import type { MemoryEntry } from '../../types/schemas';
import { MemoryEntryEditor } from './MemoryEntryEditor';
import { MemoryProvenance } from './MemoryProvenance';
import { PromoteDiff } from './PromoteDiff';
import { SCOPE_INFO, TYPE_LABELS } from './memoryModel';

export function MemoryDetailPanel({ entry }: { entry: MemoryEntry }) {
  const [editing, setEditing] = useState(false);
  const update = useUpdateMemory();
  const pin = usePinMemory();
  const del = useDeleteMemory();
  const promote = usePromoteMemory();
  const archived = entry.status === 'archived';

  return (
    <aside
      aria-label="Memory entry"
      data-testid="memory-detail"
      className="border-theme-line space-y-4 rounded border p-4 text-sm"
    >
      <div className="text-theme-muted flex flex-wrap gap-x-3 text-xs">
        <span>{TYPE_LABELS[entry.type]}</span>
        <span>{SCOPE_INFO[entry.scope].label}</span>
        <span>{entry.status}</span>
        {entry.pinned && <span className="text-theme-accent-strong font-semibold">Pinned</span>}
        <code>{entry.id}</code>
      </div>
      {editing ? (
        <MemoryEntryEditor
          entry={entry}
          onDone={() => {
            setEditing(false);
          }}
        />
      ) : (
        <p className="text-theme-text whitespace-pre-wrap">{entry.text}</p>
      )}
      {!editing && (
        <div className="flex flex-wrap gap-2">
          <Button
            size="sm"
            onClick={() => {
              setEditing(true);
            }}
          >
            Edit
          </Button>
          <Button
            size="sm"
            onClick={() => {
              pin.mutate({ id: entry.id, pinned: !entry.pinned });
            }}
          >
            {entry.pinned ? 'Unpin' : 'Pin'}
          </Button>
          <Button
            size="sm"
            onClick={() => {
              update.mutate({ id: entry.id, patch: { status: archived ? 'active' : 'archived' } });
            }}
          >
            {archived ? 'Unarchive' : 'Archive'}
          </Button>
          {entry.scope === 'private' && (
            <Button
              size="sm"
              disabled={promote.isPending}
              onClick={() => {
                promote.mutate(entry.id);
              }}
            >
              Share with the project
            </Button>
          )}
          <Button
            size="sm"
            variant="danger"
            onClick={() => {
              del.mutate(entry);
            }}
          >
            Delete
          </Button>
        </div>
      )}
      {promote.data && <PromoteDiff result={promote.data} />}
      <div>
        <p className="text-theme-muted text-xs">
          File: <code className="break-all">{entry.path}</code>{' '}
          <CopyButton text={entry.path} label="Copy the memory file path" />
        </p>
        <p className="text-theme-muted mt-1 text-xs">
          Open in editor: the dashboard cannot launch your editor, so copy the path and open the
          file there (the <code>$schema</code> gives autocomplete). The daemon rewrites the file, so
          comments are not kept.
        </p>
      </div>
      <MemoryProvenance entry={entry} />
    </aside>
  );
}
