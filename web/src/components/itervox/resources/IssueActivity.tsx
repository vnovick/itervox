import { useMemo, useState, type ReactNode } from 'react';
import { useRunActivity } from '../../../queries/activity';
import { useScopeAllows } from '../../../hooks/useCallerScope';
import { Select } from '../../ui/Select';
import { SearchInput } from '../SearchInput';
import type { ActivityReport } from '../../../types/schemas';

type Kind = 'commands' | 'files' | 'connections';

const KINDS: { id: Kind; label: string }[] = [
  { id: 'commands', label: 'Commands' },
  { id: 'files', label: 'Files' },
  { id: 'connections', label: 'Connections' },
];

const MODE_TONE: Record<string, string> = {
  precise: 'pill-ok',
  sampled: 'pill-warn',
};

const MODE_HINT: Record<string, string> = {
  sampled: 'Sampled every 2s: short-lived activity may be missed',
  precise: 'Every exec, write and connect, from eBPF tracepoints',
  off: 'Activity recording is off',
};

function matches(q: string, ...parts: (string | undefined)[]): boolean {
  if (!q) return true;
  const needle = q.toLowerCase();
  return parts.some((p) => p?.toLowerCase().includes(needle));
}

/**
 * System visibility — the issue run's activity audit: the commands it
 * started, the files it wrote or had open, and its network connections,
 * with a filter, the recording mode ("sampled" may miss short-lived
 * activity) and a warning chip for writes outside the workspace. The
 * endpoint is operator-scoped, so viewers get a note instead. Collapsed
 * until opened; polls while the run is live.
 */
export function IssueActivity({
  identifier,
  live,
  defaultOpen = false,
}: {
  identifier: string;
  live: boolean;
  defaultOpen?: boolean;
}) {
  const allowed = useScopeAllows('operator');
  const [open, setOpen] = useState(defaultOpen);
  const [runId, setRunId] = useState('');
  const [kind, setKind] = useState<Kind>('commands');
  const [query, setQuery] = useState('');
  const { data, isLoading, isError } = useRunActivity(identifier, runId, {
    enabled: open && allowed,
    live: live && runId === '',
  });
  const outside = data?.files.filter((f) => f.outsideWorkspace).length ?? 0;
  const panelId = `activity-${identifier}`;

  return (
    <section className="space-y-2" data-testid="issue-activity">
      <div className="flex flex-wrap items-center gap-2">
        <button
          type="button"
          aria-expanded={open}
          aria-controls={panelId}
          onClick={() => {
            setOpen((o) => !o);
          }}
          className="text-theme-text text-sm font-semibold hover:underline"
        >
          {open ? '▾' : '▸'} Activity
        </button>
        {data && (
          <span
            className={`pill-sm ${MODE_TONE[data.mode] ?? 'pill-neutral'}`}
            title={data.modeReason ?? MODE_HINT[data.mode]}
            data-testid="activity-mode"
          >
            {data.mode}
          </span>
        )}
        {outside > 0 && (
          <span className="pill-sm pill-warn" data-testid="activity-outside">
            {outside} {outside === 1 ? 'write' : 'writes'} outside the workspace
          </span>
        )}
      </div>
      {open && (
        <div id={panelId} className="space-y-2">
          {!allowed ? (
            <p className="text-theme-muted text-xs">
              The activity audit lists command lines and paths: it needs an operator token.
            </p>
          ) : isLoading ? (
            <p className="text-theme-muted text-xs">Loading activity…</p>
          ) : isError ? (
            <p className="text-theme-danger-text text-xs">The activity could not be loaded.</p>
          ) : !data ? (
            <p className="text-theme-muted text-xs">No activity is recorded for this issue yet.</p>
          ) : (
            <ActivityBody
              report={data}
              kind={kind}
              onKind={setKind}
              query={query}
              onQuery={setQuery}
              runId={runId}
              onRun={setRunId}
            />
          )}
        </div>
      )}
    </section>
  );
}

function ActivityBody({
  report,
  kind,
  onKind,
  query,
  onQuery,
  runId,
  onRun,
}: {
  report: ActivityReport;
  kind: Kind;
  onKind: (k: Kind) => void;
  query: string;
  onQuery: (q: string) => void;
  runId: string;
  onRun: (id: string) => void;
}) {
  const commands = useMemo(
    () => report.commands.filter((c) => matches(query, c.name, c.args)),
    [report.commands, query],
  );
  const files = useMemo(
    () => report.files.filter((f) => matches(query, f.path, f.command)),
    [report.files, query],
  );
  const connections = useMemo(
    () => report.connections.filter((c) => matches(query, c.remote, c.protocol, c.command)),
    [report.connections, query],
  );
  const counts: Record<Kind, number> = {
    commands: report.commands.length,
    files: report.files.length,
    connections: report.connections.length,
  };
  const supported = report.supports[kind];
  const dropped = report.dropped?.[kind] ?? 0;

  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        <div role="group" aria-label="Activity kind" className="segmented text-xs">
          {KINDS.map((k) => (
            <button
              key={k.id}
              type="button"
              aria-pressed={kind === k.id}
              onClick={() => {
                onKind(k.id);
              }}
              className="px-2.5 py-0.5"
            >
              {k.label} {report.supports[k.id] ? counts[k.id] : '—'}
            </button>
          ))}
        </div>
        {report.runs.length > 1 && (
          <Select
            aria-label="Run"
            value={runId}
            onChange={(e) => {
              onRun(e.target.value);
            }}
            className="w-auto text-xs"
          >
            <option value="">Latest run</option>
            {report.runs.map((r) => (
              <option key={r.runId} value={r.runId}>
                {r.live ? 'Live · ' : ''}
                {r.startedAt ? new Date(r.startedAt).toLocaleString() : r.runId}
              </option>
            ))}
          </Select>
        )}
        <SearchInput
          value={query}
          onChange={onQuery}
          label="Filter activity"
          placeholder="Filter…"
          clearLabel="Clear activity filter"
          className="min-w-[140px] flex-1"
        />
      </div>
      {!supported ? (
        <p className="text-theme-muted text-xs" data-testid="activity-unsupported">
          {kind === 'files'
            ? (report.notes.find((n) => n.includes('File activity')) ??
              'File activity is not available here.')
            : 'Not recorded in this mode.'}
        </p>
      ) : kind === 'commands' ? (
        <ActivityTable
          label="Commands"
          head={['Command', 'Runs']}
          rows={commands.map((c) => ({
            key: `${c.name} ${c.args ?? ''}`,
            cells: [
              <span key="c" className="font-mono">
                <span className="font-semibold">{c.name}</span>
                {c.args ? <span className="text-theme-text-secondary"> {c.args}</span> : null}
              </span>,
              String(c.count),
            ],
          }))}
        />
      ) : kind === 'files' ? (
        <ActivityTable
          label="Files"
          head={['Path', 'Access']}
          rows={files.map((f) => ({
            key: f.path,
            cells: [
              <span key="p" className="font-mono" title={f.command ? `by ${f.command}` : undefined}>
                {f.path}
              </span>,
              <span key="a" className="flex flex-wrap justify-end gap-1">
                <span className={`pill-sm ${f.write ? 'pill-accent' : 'pill-neutral'}`}>
                  {f.write ? 'write' : 'open'}
                </span>
                {f.outsideWorkspace && <span className="pill-sm pill-warn">outside</span>}
              </span>,
            ],
          }))}
        />
      ) : (
        <ActivityTable
          label="Connections"
          head={['Remote', 'Seen']}
          rows={connections.map((c) => ({
            key: `${c.protocol} ${c.remote}`,
            cells: [
              <span key="r" className="font-mono">
                {c.remote}
                <span className="text-theme-text-secondary">
                  {' '}
                  {[c.protocol, c.state, c.command].filter(Boolean).join(' · ')}
                </span>
              </span>,
              String(c.count),
            ],
          }))}
        />
      )}
      {dropped > 0 && (
        <p className="text-theme-muted text-xs">
          {dropped} more distinct {kind} were not kept (the per-run cap).
        </p>
      )}
      {report.notes.length > 0 && (
        <ul className="text-theme-muted space-y-0.5 text-[11px]" data-testid="activity-notes">
          {report.notes.map((n) => (
            <li key={n}>{n}</li>
          ))}
        </ul>
      )}
    </>
  );
}

function ActivityTable({
  label,
  head,
  rows,
}: {
  label: string;
  head: [string, string];
  rows: { key: string; cells: [ReactNode, ReactNode] }[];
}) {
  if (rows.length === 0) {
    return <p className="text-theme-muted text-xs">Nothing recorded.</p>;
  }
  return (
    <div className="max-h-72 overflow-y-auto">
      <table className="data-table text-xs" aria-label={label}>
        <thead>
          <tr>
            <th scope="col">{head[0]}</th>
            <th scope="col" className="text-right">
              {head[1]}
            </th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.key}>
              <td className="max-w-0 break-all">{r.cells[0]}</td>
              <td className="text-right whitespace-nowrap tabular-nums">{r.cells[1]}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
