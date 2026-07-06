import { useEffect, useMemo, useState } from 'react';
import { useShallow } from 'zustand/react/shallow';
import PageMeta from '../../components/common/PageMeta';
import { useItervoxStore } from '../../store/itervoxStore';
import { useUIStore } from '../../store/uiStore';
import { useClearIssueLogs, useIssues } from '../../queries/issues';
import { useIssueLogs, useLogIdentifiers } from '../../queries/logs';
import { orchDotClass, formatOrchestratorState } from '../../utils/format';
import { inputRequiredRowState } from '../../utils/inputRequired';
import { Terminal } from '../../components/ui/Terminal/Terminal';
import { EMPTY_RUNNING, EMPTY_RETRYING } from '../../utils/constants';
import { issueLogToTerminal } from '../../utils/logFormatting';
import type { StateSnapshot } from '../../types/schemas';
import { SearchInput } from '../../components/itervox/SearchInput';

// Logs page filter chip (T-4) treats every entry whose .message starts with
// this prefix as an automation event. The Go side writes the same prefix via
// AutomationFiredLogPrefix in internal/orchestrator/automation.go — keep the
// two in sync.
const AUTOMATION_FIRED_PREFIX = 'AUTOMATION FIRED';

// ─── Helpers ──────────────────────────────────────────────────────────────────

// AUTOMATION FIRED entries are
// tagged `event === 'automation'` (Go-side parseLogLine + Zod
// LogEventTypeSchema). `'automation'` is included in FILTER_CHIPS so the
// type-driven filter recognises the event sentinel, AND a dedicated
// `chip-automation` toggle below filters by message prefix for the visually
// distinct row treatment. The two surfaces are deliberately complementary:
// FILTER_CHIPS is the generic per-event-type chip set; chip-automation is
// the operator-facing "show me automation lines only" affordance.
const FILTER_CHIPS = ['text', 'action', 'subagent', 'warn', 'error', 'automation'] as const;
type FilterChip = (typeof FILTER_CHIPS)[number];
type InputRequiredRow = NonNullable<StateSnapshot['inputRequired']>[number];
const EMPTY_INPUT_REQUIRED: readonly InputRequiredRow[] = [];
const EMPTY_PAUSED: readonly string[] = [];

// ─── Page ─────────────────────────────────────────────────────────────────────

export default function Logs() {
  const { data: issues = [] } = useIssues();
  const logIdentifiers = useLogIdentifiers();
  const { inputRequired, paused, running, retrying } = useItervoxStore(
    useShallow((s) => ({
      inputRequired: s.snapshot?.inputRequired ?? EMPTY_INPUT_REQUIRED,
      paused: s.snapshot?.paused ?? EMPTY_PAUSED,
      running: s.snapshot?.running ?? EMPTY_RUNNING,
      retrying: s.snapshot?.retrying ?? EMPTY_RETRYING,
    })),
  );

  const inputRequiredSet = useMemo(
    () => new Set(inputRequired.map((entry) => entry.identifier)),
    [inputRequired],
  );
  const pendingResumeSet = useMemo(
    () =>
      new Set(
        inputRequired
          .filter((entry) => inputRequiredRowState(entry) === 'pending_input_resume')
          .map((entry) => entry.identifier),
      ),
    [inputRequired],
  );
  const pausedSet = useMemo(() => new Set(paused), [paused]);
  const runningSet = useMemo(() => new Set(running.map((row) => row.identifier)), [running]);
  const retryingSet = useMemo(() => new Set(retrying.map((row) => row.identifier)), [retrying]);
  const issueByIdentifier = useMemo(
    () => new Map(issues.map((issue) => [issue.identifier, issue])),
    [issues],
  );
  const allIdentifiers = useMemo(() => {
    // Build the sidebar list:
    //   - Always include LIVE issues (running / retrying / inputRequired) even
    //     before their first log line — operators want to see them immediately.
    //   - Include PAUSED + idle (review-state, blocked-by-PR) ONLY if they
    //     actually have logs in the buffer. This is what `logIdentifiers`
    //     is the source of truth for. Without this filter, clearing a
    //     review-state issue's logs leaves the row pinned because it's still
    //     in the `paused` snapshot field — confusing to the operator who
    //     just hit "Clear logs".
    const identifiers = new Set<string>();
    const logSet = new Set(logIdentifiers);
    for (const identifier of logIdentifiers) identifiers.add(identifier);
    for (const row of running) identifiers.add(row.identifier);
    for (const row of retrying) identifiers.add(row.identifier);
    for (const entry of inputRequired) identifiers.add(entry.identifier);
    for (const identifier of paused) {
      if (logSet.has(identifier)) identifiers.add(identifier);
    }
    return [...identifiers];
  }, [inputRequired, logIdentifiers, paused, retrying, running]);

  // Sidebar uses the union of issue metadata, live orchestrator state, and log
  // identifiers so active issues remain visible even before the first log line.
  const sortedIssues = useMemo(() => {
    const order = (state: string) =>
      state === 'running'
        ? 0
        : state === 'retrying'
          ? 1
          : state === 'pending_input_resume'
            ? 2
            : state === 'input_required'
              ? 3
              : state === 'paused'
                ? 4
                : 5;
    return allIdentifiers
      .map((id) => ({
        identifier: id,
        orchestratorState: runningSet.has(id)
          ? 'running'
          : retryingSet.has(id)
            ? 'retrying'
            : pendingResumeSet.has(id)
              ? 'pending_input_resume'
              : inputRequiredSet.has(id)
                ? 'input_required'
                : pausedSet.has(id)
                  ? 'paused'
                  : 'idle',
        title: issueByIdentifier.get(id)?.title ?? '',
        branchName: issueByIdentifier.get(id)?.branchName ?? null,
        agentProfile: issueByIdentifier.get(id)?.agentProfile ?? '',
      }))
      .sort((a, b) => {
        const diff = order(a.orchestratorState) - order(b.orchestratorState);
        return diff !== 0 ? diff : a.identifier.localeCompare(b.identifier);
      });
  }, [
    allIdentifiers,
    inputRequiredSet,
    issueByIdentifier,
    pausedSet,
    pendingResumeSet,
    retryingSet,
    runningSet,
  ]);

  const selectedId = useItervoxStore((s) => s.activeIssueId) ?? '';
  const setSelectedId = useItervoxStore((s) => s.setActiveIssueId);
  const [activeChips, setActiveChips] = useState<Set<FilterChip>>(new Set(FILTER_CHIPS));
  const automationOnly = useUIStore((s) => s.logsAutomationOnly);
  const setAutomationOnly = useUIStore((s) => s.setLogsAutomationOnly);
  const issueSearch = useUIStore((s) => s.logsIssueSearch);
  const setIssueSearch = useUIStore((s) => s.setLogsIssueSearch);

  const visibleIssues = useMemo(() => {
    const q = issueSearch.trim().toLowerCase();
    if (!q) return sortedIssues;
    return sortedIssues.filter((issue) =>
      [
        issue.identifier,
        issue.title,
        issue.orchestratorState,
        issue.branchName ?? '',
        issue.agentProfile,
      ]
        .join(' ')
        .toLowerCase()
        .includes(q),
    );
  }, [issueSearch, sortedIssues]);

  useEffect(() => {
    if (!selectedId || !visibleIssues.find((i) => i.identifier === selectedId)) {
      const first = visibleIssues[0];
      // eslint-disable-next-line @typescript-eslint/no-unnecessary-condition
      if (first) setSelectedId(first.identifier);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- selectedId intentionally omitted: effect only auto-selects first issue when the issue list changes, not on every selectedId transition
  }, [visibleIssues]);

  const isLive =
    running.some((r) => r.identifier === selectedId) ||
    retrying.some((r) => r.identifier === selectedId);
  const { data: entries, isLoading: loading } = useIssueLogs(selectedId, isLive);

  const clearLogsMutation = useClearIssueLogs();
  const handleClearLogs = () => {
    if (selectedId) clearLogsMutation.mutate(selectedId);
  };

  const handleExport = () => {
    const text = entries.map((e) => `${e.time ? `[${e.time}] ` : ''}${e.message}`).join('\n');
    const blob = new Blob([text], { type: 'text/plain' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `logs-${selectedId}.txt`;
    a.click();
    URL.revokeObjectURL(url);
  };

  const activeCount = sortedIssues.filter((i) => i.orchestratorState !== 'idle').length;
  const selectedIssue = sortedIssues.find((i) => i.identifier === selectedId);
  const runningRow = running.find((r) => r.identifier === selectedId);

  const toggleChip = (chip: FilterChip) => {
    setActiveChips((prev) => {
      const next = new Set(prev);
      if (next.has(chip)) next.delete(chip);
      else next.add(chip);
      return next;
    });
  };

  // Filter entries by active chips, then map to Terminal LogEntry. The
  // automation chip (T-4) is an additional gate: when active, only entries
  // whose message begins with AUTOMATION FIRED survive.
  const filteredEntries = useMemo(
    () =>
      entries.filter((e) => {
        if (
          FILTER_CHIPS.includes(e.event as FilterChip) &&
          !activeChips.has(e.event as FilterChip)
        ) {
          return false;
        }
        if (automationOnly && !e.message.startsWith(AUTOMATION_FIRED_PREFIX)) {
          return false;
        }
        return true;
      }),
    [entries, activeChips, automationOnly],
  );

  const logEntries = useMemo(() => filteredEntries.map(issueLogToTerminal), [filteredEntries]);

  return (
    <>
      <PageMeta title="Itervox | Logs" description="Agent logs — all issues" />
      <div className="flex h-[calc(100vh-64px)]">
        {/* Sidebar */}
        <div className="bg-terminal-base flex w-52 flex-shrink-0 flex-col border-r border-gray-800">
          <div className="border-b border-gray-800 px-3 py-3">
            <p className="font-mono text-[10px] font-semibold tracking-widest text-[#4b5563] uppercase">
              Issues
            </p>
            <p className="mt-0.5 font-mono text-[10px] text-[#374151]">
              {activeCount} active · {sortedIssues.length} total
            </p>
            <SearchInput
              placeholder="Search issues…"
              label="Search log issues"
              value={issueSearch}
              onChange={setIssueSearch}
              className="mt-3"
              inputClassName="h-7 border-gray-800 bg-[#080b10] font-mono text-xs text-[#9ca3af] placeholder:text-[#374151] focus:border-[#1f2937]"
            />
            {issueSearch.trim() !== '' && (
              <p className="mt-1 font-mono text-[10px] text-[#374151]">
                {visibleIssues.length} match{visibleIssues.length === 1 ? '' : 'es'}
              </p>
            )}
          </div>
          <div className="flex-1 overflow-y-auto">
            {sortedIssues.length === 0 && (
              <p className="px-3 py-4 font-mono text-xs text-[#374151]">No issues loaded</p>
            )}
            {sortedIssues.length > 0 && visibleIssues.length === 0 && (
              <p className="px-3 py-4 font-mono text-xs text-[#374151]">No matching issues</p>
            )}
            {visibleIssues.map((issue) => (
              <button
                key={issue.identifier}
                onClick={() => {
                  setSelectedId(issue.identifier);
                }}
                className={`flex w-full items-center gap-2 border-b border-gray-900 px-3 py-2 text-left font-mono text-xs transition-colors ${
                  selectedId === issue.identifier ? 'bg-[#1a1f2e]' : 'hover:bg-[#161a22]'
                }`}
              >
                <span
                  className={`h-1.5 w-1.5 flex-shrink-0 rounded-full ${orchDotClass(issue.orchestratorState)}`}
                />
                <span
                  className={`truncate ${
                    selectedId === issue.identifier ? 'text-[#4ade80]' : 'text-[#9ca3af]'
                  }`}
                >
                  {issue.identifier}
                </span>
              </button>
            ))}
          </div>
        </div>

        {/* Terminal panel */}
        <div className="bg-terminal-void flex flex-1 flex-col overflow-hidden">
          {/* Terminal title bar */}
          <div className="bg-terminal-header flex flex-shrink-0 items-center justify-between border-b border-[#1e2420] px-4 py-2">
            <div className="flex items-center gap-3">
              {/* Traffic light dots */}
              <span className="flex gap-1.5">
                <span className="h-3 w-3 rounded-full bg-[#ff5f57]" />
                <span className="h-3 w-3 rounded-full bg-[#febc2e]" />
                <span className="h-3 w-3 rounded-full bg-[#28c840]" />
              </span>
              <span className="font-mono text-xs text-[#4b5563]">
                {selectedId ? (
                  <>
                    <span className="text-[#4ade80]">{selectedId}</span>
                    {selectedIssue && (
                      <span className="ml-2 text-[#374151]">
                        — {formatOrchestratorState(selectedIssue.orchestratorState)}
                        {loading && <span className="ml-2">· refreshing…</span>}
                      </span>
                    )}
                  </>
                ) : (
                  <span className="text-[#374151]">select an issue</span>
                )}
              </span>
            </div>
            <div className="flex items-center gap-3">
              {entries.length > 0 && (
                <span className="font-mono text-[10px] text-[#374151]">{entries.length} lines</span>
              )}
              {entries.length > 0 && (
                <button
                  onClick={handleClearLogs}
                  className="font-mono text-[10px] text-[#4b5563] transition-colors hover:text-[#ef4444]"
                >
                  ✕ clear
                </button>
              )}
              {entries.length > 0 && (
                <button
                  onClick={handleExport}
                  className="font-mono text-[10px] text-[#4b5563] transition-colors hover:text-[#9ca3af]"
                >
                  ↓ export
                </button>
              )}
            </div>
          </div>

          {/* Contextual strip (5.1): state | branch | profile | host | session */}
          {selectedId && (
            <div
              data-testid="logs-context-strip"
              className="bg-terminal-header flex flex-shrink-0 flex-wrap items-center gap-x-4 gap-y-1 border-b border-[#1e2420] px-4 py-1.5"
            >
              <span className="font-mono text-[10px] text-[#4b5563]">
                state{' '}
                <span className="text-[#9ca3af]">
                  {formatOrchestratorState(selectedIssue?.orchestratorState ?? 'idle')}
                </span>
              </span>
              {selectedIssue?.branchName && (
                <span className="font-mono text-[10px] text-[#4b5563]">
                  branch <span className="text-[#9ca3af]">{selectedIssue.branchName}</span>
                </span>
              )}
              {selectedIssue?.agentProfile && (
                <span className="font-mono text-[10px] text-[#4b5563]">
                  profile <span className="text-[#9ca3af]">{selectedIssue.agentProfile}</span>
                </span>
              )}
              {runningRow?.workerHost && (
                <span className="font-mono text-[10px] text-[#4b5563]">
                  host <span className="text-[#9ca3af]">{runningRow.workerHost}</span>
                </span>
              )}
              {runningRow?.sessionId && (
                <span className="font-mono text-[10px] text-[#4b5563]">
                  session <span className="text-[#9ca3af]">{runningRow.sessionId.slice(0, 8)}</span>
                </span>
              )}
            </div>
          )}

          {/* Quick filter chips (5.2) */}
          {selectedId && (
            <div
              data-testid="logs-filter-chips"
              className="bg-terminal-header flex flex-shrink-0 items-center gap-2 border-b border-[#1e2420] px-4 py-1.5"
            >
              {FILTER_CHIPS.map((chip) => (
                <button
                  key={chip}
                  data-testid={`chip-${chip}`}
                  onClick={() => {
                    toggleChip(chip);
                  }}
                  className={`rounded px-2 py-0.5 font-mono text-[10px] transition-colors ${
                    activeChips.has(chip)
                      ? 'bg-[#1a1f2e] text-[#9ca3af]'
                      : 'text-[#374151] line-through'
                  }`}
                >
                  {chip}
                </button>
              ))}
              <button
                type="button"
                // codex-B5: dedicated automation-only toggle. Renamed from
                // `chip-automation` to `chip-automation-only` so the now-
                // included FILTER_CHIPS 'automation' entry can keep the
                // standard `chip-${name}` testid pattern without colliding.
                data-testid="chip-automation-only"
                aria-pressed={automationOnly}
                onClick={() => {
                  setAutomationOnly(!automationOnly);
                }}
                className={`rounded px-2 py-0.5 font-mono text-[10px] transition-colors ${
                  automationOnly
                    ? 'bg-emerald-500/20 text-emerald-300'
                    : 'text-[#374151] line-through'
                }`}
                title="Show only AUTOMATION FIRED entries"
              >
                automation
              </button>
              <span className="ml-auto font-mono text-[10px] text-[#374151]">
                {filteredEntries.length} / {entries.length}
              </span>
            </div>
          )}

          {/* Log output via Terminal (5.3) */}
          <div className="flex flex-1 flex-col overflow-hidden">
            {!selectedId ? (
              <div className="flex flex-1 items-center justify-center font-mono text-xs text-[#374151]">
                $ select an issue from the sidebar
              </div>
            ) : logEntries.length === 0 && !loading ? (
              <div className="flex flex-1 items-center justify-center font-mono text-xs text-[#4b5563]">
                $ waiting for agent output…
              </div>
            ) : (
              <Terminal entries={logEntries} follow showTime={false} />
            )}
          </div>
        </div>
      </div>
    </>
  );
}
