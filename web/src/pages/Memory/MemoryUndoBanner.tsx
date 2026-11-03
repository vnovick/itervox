// Memory page undo (#202): after a delete, one click puts the entry back
// exactly as it was (same id, provenance and usage).

import { Banner } from '../../components/ui/Banner';
import { Button } from '../../components/ui/button';
import { useCreateMemory } from '../../queries/memory';
import { useUIStore } from '../../store/uiStore';

export function MemoryUndoBanner() {
  const deleted = useUIStore((s) => s.memoryUndo);
  const setUndo = useUIStore((s) => s.setMemoryUndo);
  const restore = useCreateMemory();
  if (!deleted) return null;
  return (
    <Banner tone="info" className="flex items-center justify-between gap-3 rounded">
      <span className="truncate">Deleted “{deleted.text}”.</span>
      <span className="flex shrink-0 gap-2">
        <Button
          size="sm"
          disabled={restore.isPending}
          onClick={() => {
            restore.mutate(deleted, {
              onSuccess: () => {
                setUndo(null);
              },
            });
          }}
        >
          Undo
        </Button>
        <Button
          size="sm"
          variant="ghost"
          aria-label="Dismiss"
          onClick={() => {
            setUndo(null);
          }}
        >
          ✕
        </Button>
      </span>
    </Banner>
  );
}
