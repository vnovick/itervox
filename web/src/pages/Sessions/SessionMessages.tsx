import type { SessionMessageRow } from '../../types/schemas';

const EMPTY_MESSAGES: readonly SessionMessageRow[] = [];

function statusLabel(m: SessionMessageRow): string {
  if (m.status === 'delivered') {
    return m.delivery === 'live' ? 'delivered live' : 'delivered on resume';
  }
  if (m.status === 'dropped') return m.reason ? `not delivered: ${m.reason}` : 'not delivered';
  return 'queued';
}

/** The operator messages sent to a session and where each one is. */
export function SessionMessages({
  messages = EMPTY_MESSAGES,
}: {
  messages?: readonly SessionMessageRow[];
}) {
  if (messages.length === 0) return null;
  return (
    <ul aria-label="Messages sent" className="space-y-1 text-xs">
      {messages.map((m) => (
        <li
          key={m.id}
          data-testid="session-message"
          data-status={m.status}
          className="border-theme-line flex items-start justify-between gap-3 rounded border px-2 py-1"
        >
          <span className="text-theme-text whitespace-pre-wrap">{m.text}</span>
          <span
            className={
              m.status === 'dropped'
                ? 'text-theme-danger-text shrink-0'
                : m.status === 'delivered'
                  ? 'text-theme-success-text shrink-0'
                  : 'text-theme-muted shrink-0'
            }
          >
            {statusLabel(m)}
          </span>
        </li>
      ))}
    </ul>
  );
}
