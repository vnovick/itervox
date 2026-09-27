/**
 * Module-level registry shared by every dialog (CORE-067).
 *
 * Two independent concerns live here because both need a single owner for
 * the whole page:
 *
 *  1. The overlay stack. Dialogs register when they open, in open order. Only
 *     the top-most layer handles Escape and runs an active focus trap, so a
 *     Modal opened over a SlidePanel closes alone and Tab stays inside it.
 *     One document-level keydown listener serves the whole stack.
 *  2. The body scroll lock. It is ref-counted by owner id and idempotent per
 *     owner: acquiring twice or releasing twice is a no-op. That keeps the
 *     count correct under StrictMode's mount/unmount/mount replay and when
 *     dialogs close out of order. The pre-lock `overflow` value is saved when
 *     the first owner acquires and restored when the last owner releases.
 */

type Listener = () => void;

interface Layer {
  id: string;
  onEscape: () => void;
}

const layers: Layer[] = [];
const listeners = new Set<Listener>();

function emit() {
  for (const l of listeners) l();
}

function handleDocumentKeyDown(event: KeyboardEvent) {
  if (event.key !== 'Escape' || event.defaultPrevented) return;
  const top = layers[layers.length - 1] as Layer | undefined;
  if (!top) return;
  top.onEscape();
}

/** Registers (or re-registers) a dialog as the top-most layer. */
export function pushLayer(id: string, onEscape: () => void): void {
  const existing = layers.findIndex((l) => l.id === id);
  if (existing !== -1) layers.splice(existing, 1);
  if (layers.length === 0) document.addEventListener('keydown', handleDocumentKeyDown);
  layers.push({ id, onEscape });
  emit();
}

/** Removes a dialog from the stack wherever it sits. Unknown ids are ignored. */
export function removeLayer(id: string): void {
  const idx = layers.findIndex((l) => l.id === id);
  if (idx === -1) return;
  layers.splice(idx, 1);
  if (layers.length === 0) document.removeEventListener('keydown', handleDocumentKeyDown);
  emit();
}

export function topLayerId(): string | null {
  return layers.length > 0 ? layers[layers.length - 1].id : null;
}

export function subscribeLayers(listener: Listener): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

// ─── Scroll lock ────────────────────────────────────────────────────────────

const lockOwners = new Set<string>();
let savedOverflow = '';

export function acquireScrollLock(owner: string): void {
  if (lockOwners.has(owner)) return;
  if (lockOwners.size === 0) {
    savedOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
  }
  lockOwners.add(owner);
}

export function releaseScrollLock(owner: string): void {
  if (!lockOwners.delete(owner)) return;
  if (lockOwners.size === 0) document.body.style.overflow = savedOverflow;
}

/** Test-only visibility into the lock count. */
export function scrollLockOwnerCount(): number {
  return lockOwners.size;
}
