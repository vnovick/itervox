import { useMemo, useState } from 'react';
import { useShallow } from 'zustand/react/shallow';
import PageMeta from '../../components/common/PageMeta';
import { useItervoxStore } from '../../store/itervoxStore';
import { useUIStore } from '../../store/uiStore';
import { useClearIssueLogs, useIssues } from '../../queries/issues';
import { useIssueLogs, useLogIdentifiers } from '../../queries/logs';
import { useLogsUrlSelection } from '../../hooks/useUrlState';
import { orchDotClass, formatOrchestratorState } from '../../utils/format';
import { inputRequiredRowState } from '../../utils/inputRequired';
import { Terminal } from '../../components/ui/Terminal/Terminal';
import { ConfirmButton } from '../../components/ui/button/ConfirmButton';
import { EMPTY_RUNNING, EMPTY_RETRYING } from '../../utils/constants';
import { issueLogToTerminal } from '../../utils/logFormatting';
import type { StateSnapshot, TrackerIssue } from '../../types/schemas';
import { SearchInput } from '../../components/itervox/SearchInput';
import { Button } from '../../components/ui/button';
import { Select } from '../../components/ui/Select';
import { ToggleChip } from '../../components/ui/ToggleChip';
import { Banner } from '../../components/ui/Banner';
import { EmptyState } from '../../components/ui/EmptyState';
import { cn } from '../../components/ui/cn';

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
const EMPTY_ISSUES: readonly TrackerIssue[] = [];

// ─── Page ─────────────────────────────────────────────────────────────────────

export default function Logs() {
  const issuesQuery = useIssues();
  const issues = issuesQuery.data ?? EMPTY_ISSUES;
  const { data: logIdentifiers, settled: logIdentifiersSettled } = useLogIdentifiers();
  const { hasSnapshot, inputRequired, paused, running, retrying } = useItervoxStore(
    useShallow((s) => ({
      hasSnapshot: s.snapshot !== null,
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
    // CORE-082 — attention first: an issue waiting on the operator sorts
    // above running work.
    const order = (state: string) =>
      state === 'input_required'
        ? 0
        : state === 'running'
          ? 1
          : state === 'retrying'
            ? 2
            : state === 'pending_input_resume'
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

  // CORE-085 — the selection lives in /logs/:identifier. The URL id wins over
  // the first-row auto-select; it is only abandoned once issues, the snapshot
  // and the log identifiers have loaded without it (the unfiltered union is
  // checked, so a search that hides the row does not move the selection).
  const settled =
    (issuesQuery.isSuccess || issuesQuery.isError) && hasSnapshot && logIdentifiersSettled;
  const visibleIds = useMemo(() => visibleIssues.map((i) => i.identifier), [visibleIssues]);
  const { selectedId, select: setSelectedId } = useLogsUrlSelection({
    union: allIdentifiers,
    visible: visibleIds,
    settled,
  });

  const isLive =
    running.some((r) => r.identifier === selectedId) ||
    retrying.some((r) => r.identifier === selectedId);
  const { data: entries, isLoading: loading, isError } = useIssueLogs(selectedId, isLive);

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
  const hiddenCount = entries.length - filteredEntries.length;

  const selectIssue = (id: string) => {
    setSelectedId(id);
  };

  return (
    <>
      <PageMeta title="Itervox | Logs" description="Agent logs — all issues" />
      {/* CORE-087 — below md the sidebar becomes a select above the terminal;
          dvh so mobile browser chrome does not push the pane off-screen. */}
      <div className="flex h-[calc(100dvh-64px)] min-h-0 flex-col md:flex-row">
        {/* Sidebar (md and up) */}
        <div
          data-testid="logs-sidebar"
          className="bg-theme-panel border-theme-line hidden w-52 flex-shrink-0 flex-col border-r md:flex"
        >
          <div className="border-theme-line border-b px-3 py-3">
            <p className="text-theme-text-secondary font-mono text-[10px] font-semibold tracking-widest uppercase">
              Issues
            </p>
            <p className="text-theme-muted mt-0.5 font-mono text-[10px]">
              {activeCount} active · {sortedIssues.length} total
            </p>
            <SearchInput
              placeholder="Search issues…"
              label="Search log issues"
              value={issueSearch}
              onChange={setIssueSearch}
              className="mt-3"
              inputClassName="h-7 font-mono text-xs"
            />
            {issueSearch.trim() !== '' && (
              <p className="text-theme-muted mt-1 font-mono text-[10px]">
                {visibleIssues.length} match{visibleIssues.length === 1 ? '' : 'es'}
              </p>
            )}
          </div>
          <div className="flex-1 overflow-y-auto">
            {sortedIssues.length === 0 && (
              <p className="text-theme-muted px-3 py-4 font-mono text-xs">No issues loaded</p>
            )}
            {sortedIssues.length > 0 && visibleIssues.length === 0 && (
              <p className="text-theme-muted px-3 py-4 font-mono text-xs">No matching issues</p>
            )}
            {visibleIssues.map((issue) => {
              const active = selectedId === issue.identifier;
              return (
                <Button
                  key={issue.identifier}
                  variant="ghost"
                  size="sm"
                  aria-current={active ? 'true' : undefined}
                  onClick={() => {
                    selectIssue(issue.identifier);
                  }}
                  className={cn(
                    'border-theme-line h-auto w-full justify-start gap-2 rounded-none border-b px-3 py-2 text-left font-mono',
                    active && 'bg-theme-accent-soft text-theme-accent-strong',
                  )}
                >
                  <span
                    aria-hidden="true"
                    className={`h-1.5 w-1.5 flex-shrink-0 rounded-full ${orchDotClass(issue.orchestratorState)}`}
                  />
                  <span className="truncate">{issue.identifier}</span>
                </Button>
              );
            })}
          </div>
        </div>

        {/* Issue picker (below md) */}
        <div className="bg-theme-panel border-theme-line flex flex-shrink-0 items-center gap-2 border-b px-3 py-2 md:hidden">
          <label
            htmlFor="logs-issue-select"
            className="text-theme-text-secondary font-mono text-[11px]"
          >
            Issue
          </label>
          <Select
            id="logs-issue-select"
            data-testid="logs-issue-select"
            value={selectedId}
            onChange={(event) => {
              selectIssue(event.target.value);
            }}
            className="h-8 min-w-0 flex-1 font-mono text-xs"
          >
            {selectedId === '' && <option value="">Select an issue</option>}
            {sortedIssues.map((issue) => (
              <option key={issue.identifier} value={issue.identifier}>
                {issue.identifier} — {formatOrchestratorState(issue.orchestratorState)}
              </option>
            ))}
          </Select>
        </div>

        {/* Terminal panel */}
        <div className="bg-theme-bg flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
          {/* Title bar */}
          <div className="bg-theme-panel border-theme-line flex flex-shrink-0 flex-wrap items-center justify-between gap-2 border-b px-4 py-2">
            <div className="flex min-w-0 items-center gap-3">
              <span className="text-theme-text-secondary truncate font-mono text-xs">
                {selectedId ? (
                  <>
                    <span data-testid="logs-selected-id" className="text-theme-success-text">
                      {selectedId}
                    </span>
                    {selectedIssue && (
                      <span className="text-theme-muted ml-2">
                        — {formatOrchestratorState(selectedIssue.orchestratorState)}
                        {loading && <span className="ml-2">· refreshing…</span>}
                      </span>
                    )}
                  </>
                ) : (
                  <span className="text-theme-muted">select an issue</span>
                )}
              </span>
            </div>
            <div className="flex items-center gap-3">
              {entries.length > 0 && (
                <span className="text-theme-muted font-mono text-[10px]">
                  {entries.length} lines
                </span>
              )}
              {entries.length > 0 && (
                <ConfirmButton
                  label="✕ clear"
                  confirmLabel="Yes, clear"
                  pendingLabel="Clearing…"
                  isPending={clearLogsMutation.isPending}
                  onConfirm={handleClearLogs}
                />
              )}
              {entries.length > 0 && (
                <Button variant="ghost" size="xs" onClick={handleExport} className="font-mono">
                  ↓ export
                </Button>
              )}
            </div>
          </div>

          {/* Contextual strip (5.1): state | branch | profile | host | session */}
          {selectedId && (
            <div
              data-testid="logs-context-strip"
              className="bg-theme-panel border-theme-line text-theme-muted flex flex-shrink-0 flex-wrap items-center gap-x-4 gap-y-1 border-b px-4 py-1.5 font-mono text-[10px]"
            >
              <span>
                state{' '}
                <span className="text-theme-text-secondary">
                  {formatOrchestratorState(selectedIssue?.orchestratorState ?? 'idle')}
                </span>
              </span>
              {selectedIssue?.branchName && (
                <span>
                  branch{' '}
                  <span className="text-theme-text-secondary">{selectedIssue.branchName}</span>
                </span>
              )}
              {selectedIssue?.agentProfile && (
                <span>
                  profile{' '}
                  <span className="text-theme-text-secondary">{selectedIssue.agentProfile}</span>
                </span>
              )}
              {runningRow?.workerHost && (
                <span>
                  host <span className="text-theme-text-secondary">{runningRow.workerHost}</span>
                </span>
              )}
              {runningRow?.sessionId && (
                <span>
                  session{' '}
                  <span className="text-theme-text-secondary">
                    {runningRow.sessionId.slice(0, 8)}
                  </span>
                </span>
              )}
            </div>
          )}

          {/* Quick filter chips (5.2) */}
          {selectedId && (
            <div
              data-testid="logs-filter-chips"
              className="bg-theme-panel border-theme-line flex flex-shrink-0 flex-wrap items-center gap-2 border-b px-4 py-1.5"
            >
              {FILTER_CHIPS.map((chip) => (
                <ToggleChip
                  key={chip}
                  data-testid={`chip-${chip}`}
                  pressed={activeChips.has(chip)}
                  onPressedChange={() => {
                    toggleChip(chip);
                  }}
                >
                  {chip}
                </ToggleChip>
              ))}
              <ToggleChip
                // codex-B5: dedicated automation-only toggle. Renamed from
                // `chip-automation` to `chip-automation-only` so the
                // FILTER_CHIPS 'automation' entry keeps the standard
                // `chip-${name}` testid pattern without colliding.
                data-testid="chip-automation-only"
                pressed={automationOnly}
                onPressedChange={setAutomationOnly}
                className={cn(automationOnly && 'bg-theme-success-soft text-theme-success-text')}
                title="Show only AUTOMATION FIRED entries"
              >
                automation only
              </ToggleChip>
              <span className="text-theme-muted ml-auto font-mono text-[10px]">
                {filteredEntries.length} / {entries.length}
              </span>
            </div>
          )}

          {/* CORE-082 — a failed fetch / dropped stream is an error, not "no
              output yet". Lines already received stay visible below it. */}
          {selectedId && isError && (
            <Banner tone="danger" data-testid="logs-error-state" className="font-mono">
              {isLive
                ? '$ log stream lost — retrying automatically'
                : '$ could not load logs for this issue'}
            </Banner>
          )}

          {/* Log output via Terminal (5.3) */}
          <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
            {!selectedId ? (
              <EmptyState title="$ select an issue from the sidebar" className="font-mono" />
            ) : logEntries.length === 0 && hiddenCount > 0 ? (
              <EmptyState
                data-testid="logs-filtered-empty"
                className="font-mono"
                title={`$ ${String(hiddenCount)} ${hiddenCount === 1 ? 'entry' : 'entries'} hidden by filters`}
              />
            ) : logEntries.length === 0 && isError ? null : logEntries.length === 0 && !loading ? (
              <EmptyState title="$ waiting for agent output…" className="font-mono" />
            ) : (
              <Terminal
                entries={logEntries}
                follow
                showTime={false}
                // CORE-069: a different issue or filter is a new view.
                resetKey={`${selectedId}|${[...activeChips].sort().join(',')}|${String(automationOnly)}`}
              />
            )}
          </div>
        </div>
      </div>
    </>
  );
}
