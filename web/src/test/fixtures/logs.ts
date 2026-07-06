// Log entry fixtures. `makeLogEntry` covers every variant of LogEventTypeSchema
// so a renderer test can ensure each event type maps to a visible row.

import { IssueLogEntrySchema, type IssueLogEntry, type LogEventType } from '../../types/schemas';
import { applyOverrides, type DeepPartial } from './deepPartial';
import { formatRFC3339, secondsAgo } from './time';

const DEFAULTS_BY_EVENT: Record<
  LogEventType,
  Pick<IssueLogEntry, 'message' | 'tool' | 'detail'>
> = {
  text: { message: 'Assistant message text.' },
  action: { message: 'Tool invocation.', tool: 'Read', detail: 'src/index.ts' },
  subagent: { message: 'Subagent invoked.', tool: 'Task' },
  pr: { message: 'Opened PR.', detail: 'https://example.com/pr/1' },
  turn: { message: 'Turn 3 starting.' },
  warn: { message: 'Soft warning.' },
  info: { message: 'Informational note.' },
  error: { message: 'Tool error.', detail: 'permission denied' },
  // Orchestrator's AUTOMATION FIRED dispatch block,
  // surfaced as a first-class log event the dashboard chip can toggle.
  automation: {
    message:
      'AUTOMATION FIRED · clarify-blocked-issues\n  trigger: input_required\n  context: needs answer\n  profile: input-responder',
  },
};

export function makeLogEntry(
  event: LogEventType = 'info',
  overrides?: DeepPartial<IssueLogEntry>,
): IssueLogEntry {
  const defaults = DEFAULTS_BY_EVENT[event];
  const base: IssueLogEntry = {
    level: event === 'error' ? 'error' : event === 'warn' ? 'warn' : 'info',
    event,
    message: defaults.message,
    tool: defaults.tool,
    detail: defaults.detail,
    time: formatRFC3339(secondsAgo(5)),
  };
  return IssueLogEntrySchema.parse(applyOverrides(base, overrides));
}

export function makeAllEventTypes(): IssueLogEntry[] {
  const events: LogEventType[] = [
    'text',
    'action',
    'subagent',
    'pr',
    'turn',
    'warn',
    'info',
    'error',
    'automation',
  ];
  return events.map((e) => makeLogEntry(e));
}

export function makeSubLogEntries(count: number, sessionId = 'sess-fixture'): IssueLogEntry[] {
  return Array.from({ length: count }, (_v, i) =>
    makeLogEntry('text', {
      sessionId,
      message: `Sublog line ${String(i + 1)}`,
      time: formatRFC3339(secondsAgo(count - i)),
    }),
  );
}
