import type { SupportedBackend } from '../profileCommands';

export function backendLabel(backend: SupportedBackend): string {
  return backend === 'codex' ? 'Codex' : 'Claude';
}

export function backendBadgeClass(backend: SupportedBackend): string {
  return backend === 'codex'
    ? 'bg-[var(--teal-soft)] text-[var(--teal)]'
    : 'bg-[var(--accent-soft)] text-[var(--accent-strong)]';
}
