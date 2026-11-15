import { backendLabel as labelFor } from '../../../lib/backends';
import type { SupportedBackend } from '../profileCommands';

export function backendLabel(backend: SupportedBackend): string {
  return labelFor(backend);
}

export function backendBadgeClass(backend: SupportedBackend): string {
  switch (backend) {
    case 'codex':
      return 'bg-[var(--teal-soft)] text-[var(--teal)]';
    case 'claude':
      return 'bg-[var(--accent-soft)] text-[var(--accent-strong)]';
    default:
      return 'bg-theme-chip text-theme-text-secondary';
  }
}
