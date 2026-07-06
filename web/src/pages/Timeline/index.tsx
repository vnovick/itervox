import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import PageMeta from '../../components/common/PageMeta';
import { useItervoxStore } from '../../store/itervoxStore';
import { useUIStore } from '../../store/uiStore';
import { useIssueLogs, useSubagentLogs } from '../../queries/logs';
import { useStableValue } from '../../hooks/useStableValue';
import { useClearIssueLogs, useClearIssueSubLogs } from '../../queries/issues';

import {
  EMPTY_RUNNING,
  EMPTY_HISTORY,
  dedupSessionsPreferLive,
  fromRunning,
  fromHistory,
  extractSubagents,
  filterByRun,
} from '../../components/itervox/timeline/types';
import type { NormalisedSession } from '../../components/itervox/timeline/types';

import { TimelineSidebar } from './components/TimelineSidebar';
import { TimelineDetailPanel } from './components/TimelineDetailPanel';

// ─── Main page ────────────────────────────────────────────────────────────────

export default function Timeline() {
  const rawRunning = useItervoxStore((s) => s.snapshot?.running ?? EMPTY_RUNNING);
  const rawHistory = useItervoxStore((s) => s.snapshot?.history ?? EMPTY_HISTORY);
  const currentAppSessionId = useItervoxStore((s) => s.snapshot?.currentAppSessionId);
  const liveRunning = useStableValue(rawRunning, 5000);

  const liveSessions = useMemo(() => liveRunning.map(fromRunning), [liveRunning]);
  const historySessions = useMemo(() => rawHistory.map(fromHistory), [rawHistory]);
  const automationOnly = useUIStore((s) => s.timelineAutomationOnly);
  const setAutomationOnly = useUIStore((s) => s.setTimelineAutomationOnly);

  const allSessions = useMemo<NormalisedSession[]>(() => {
    // dedupSessionsPreferLive guards the ~5s stale-running window where the
    // same sessionId can appear in both `running` (via useStableValue) and
    // `history`, which would double-count sidebar runs. G-13 relay.
    const sorted = dedupSessionsPreferLive([...historySessions, ...liveSessions]).sort(
      (a, b) => new Date(a.startedAt).getTime() - new Date(b.startedAt).getTime(),
    );
    return automationOnly
      ? sorted.filter((s) => s.automationId !== undefined && s.automationId !== '')
      : sorted;
  }, [historySessions, liveSessions, automationOnly]);

  const hasAnyAutomationRun = useMemo(
    () => historySessions.some((s) => s.automationId) || liveSessions.some((s) => s.automationId),
    [historySessions, liveSessions],
  );

  const issueGroups = useMemo(() => {
    const map = new Map<string, NormalisedSession[]>();
    for (const s of allSessions) {
      map.set(s.identifier, [...(map.get(s.identifier) ?? []), s]);
    }
    return Array.from(map.entries())
      .map(([identifier, runs]) => {
        const hasLive = runs.some((r) => r.status === 'live');
        const hasFailed = runs.some((r) => r.status === 'failed');
        const hasInputRequired = runs.some((r) => r.status === 'input_required');
        const latestStatus: NormalisedSession['status'] = hasLive
          ? 'live'
          : hasInputRequired
            ? 'input_required'
            : hasFailed
              ? 'failed'
              : runs.every((r) => r.status === 'succeeded')
                ? 'succeeded'
                : runs.some((r) => r.status === 'stalled')
                  ? 'stalled'
                  : 'cancelled';
        const latestStartedAt = runs[runs.length - 1].startedAt;
        return { identifier, runs, latestStatus, latestStartedAt };
      })
      .sort((a, b) => {
        if (a.latestStatus === 'live' && b.latestStatus !== 'live') return -1;
        if (b.latestStatus === 'live' && a.latestStatus !== 'live') return 1;
        return new Date(b.latestStartedAt).getTime() - new Date(a.latestStartedAt).getTime();
      });
  }, [allSessions]);

  // ── Clear log mutations ───────────────────────────────────────────────────
  const clearIssueLogs = useClearIssueLogs();
  const clearIssueSubLogs = useClearIssueSubLogs();
  const [confirmClearId, setConfirmClearId] = useState<string | null>(null);

  // ── Selection ─────────────────────────────────────────────────────────────
  const selectedId = useItervoxStore((s) => s.activeIssueId);
  const setSelectedId = useItervoxStore((s) => s.setActiveIssueId);
  const [expandedRunAt, setExpandedRunAt] = useState<string | null>(null);
  const [selectedSubagentIdx, setSelectedSubagentIdx] = useState<number | null>(null);

  // Track which selectedId we last synced expansion state for
  const lastExpandedForIdRef = useRef<string | null>(null);

  // Auto-select first issue when current selection is invalid.
  const groupIds = useMemo(() => issueGroups.map((g) => g.identifier).join('\0'), [issueGroups]);
  const firstGroupId = issueGroups[0]?.identifier ?? null;
  useEffect(() => {
    if (!firstGroupId) return;
    if (!selectedId || !groupIds.includes(selectedId)) {
      setSelectedId(firstGroupId);
    }
  }, [groupIds, firstGroupId, selectedId, setSelectedId]);

  // When selectedId changes, auto-expand the latest run and clear subagent selection.
  useEffect(() => {
    if (selectedId === lastExpandedForIdRef.current) return;
    lastExpandedForIdRef.current = selectedId;
    setSelectedSubagentIdx(null);
    const group = issueGroups.find((g) => g.identifier === selectedId);
    if (group && group.runs.length > 0) {
      const latest = [...group.runs].sort(
        (a, b) => new Date(b.startedAt).getTime() - new Date(a.startedAt).getTime(),
      )[0];
      setExpandedRunAt(latest.startedAt);
    } else {
      setExpandedRunAt(null);
    }
  }, [selectedId, issueGroups]);

  // ── Viewport ──────────────────────────────────────────────────────────────
  const [viewport, setViewport] = useState<{ start: number; end: number } | null>(null);
  const lastViewportForIdRef = useRef<string | null>(selectedId);

  useEffect(() => {
    if (selectedId !== lastViewportForIdRef.current) {
      lastViewportForIdRef.current = selectedId;
      setViewport(null);
    }
  }, [selectedId]);

  const selectedGroup = useMemo(
    () => issueGroups.find((g) => g.identifier === selectedId) ?? null,
    [issueGroups, selectedId],
  );

  const isSelectedLive = selectedGroup?.runs.some((r) => r.status === 'live') ?? false;
  const { data: logsForSelected } = useIssueLogs(selectedId ?? '', isSelectedLive);
  const { data: sublogs = [] } = useSubagentLogs(selectedId ?? '', isSelectedLive);

  const [liveTick, setLiveTick] = useState(0);

  const { wantStart, wantEnd } = useMemo(() => {
    const now = Date.now();
    const runs = selectedGroup?.runs ?? [];
    const times = runs.map((r) => new Date(r.startedAt).getTime());
    const earliest = times.length > 0 ? Math.min(...times) : now - 10 * 60_000;
    const ws = earliest - 2 * 60_000;
    const hasLive = runs.some((r) => r.status === 'live');
    const we = hasLive
      ? now + 10 * 60_000
      : runs.length > 0
        ? Math.max(
            ...runs.map((r) =>
              r.finishedAt
                ? new Date(r.finishedAt).getTime()
                : new Date(r.startedAt).getTime() + r.elapsedMs,
            ),
          ) +
          2 * 60_000
        : now + 10 * 60_000;
    return { wantStart: ws, wantEnd: we };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedGroup, liveTick]);

  useEffect(() => {
    setViewport((prev) =>
      prev
        ? { start: Math.min(prev.start, wantStart), end: Math.max(prev.end, wantEnd) }
        : { start: wantStart, end: wantEnd },
    );
  }, [wantStart, wantEnd]);

  // Keep the axis on the selected issue's full session span. Expanding a run
  // changes the log drilldown only; it must not move other run rows under a
  // different time scale.
  const viewStart = (viewport ?? { start: wantStart, end: wantEnd }).start;
  const viewEnd = (viewport ?? { start: wantStart, end: wantEnd }).end;

  // ── Log panel data ────────────────────────────────────────────────────────
  const expandedRun = selectedGroup?.runs.find((r) => r.startedAt === expandedRunAt) ?? null;
  const expandedSessionId = expandedRun?.sessionId;
  const subagentsForExpanded = useMemo(
    () => extractSubagents(logsForSelected, expandedSessionId),
    [logsForSelected, expandedSessionId],
  );

  const expandedRunFinishedAt = expandedRun?.finishedAt;

  const sublogsForExpanded = useMemo(() => {
    if (!expandedRun) return [];
    if (sublogs.length === 0) return [];
    const hasSessionIds = sublogs.some((e) => e.sessionId);
    if (!hasSessionIds) return sublogs;
    return filterByRun(sublogs, expandedRun);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sublogs, expandedSessionId, expandedRunAt, expandedRunFinishedAt]);

  const validSubagentIdx =
    selectedSubagentIdx !== null &&
    selectedSubagentIdx >= 0 &&
    selectedSubagentIdx < subagentsForExpanded.length
      ? selectedSubagentIdx
      : null;
  const logPanelSlice = !expandedRun
    ? []
    : validSubagentIdx !== null
      ? subagentsForExpanded[validSubagentIdx].logSlice
      : sublogsForExpanded.length > 0 && expandedSessionId
        ? sublogsForExpanded
        : filterByRun(logsForSelected, expandedRun);
  const selectedSession = selectedGroup?.runs.find((r) => r.startedAt === expandedRunAt) ?? null;
  const anyLive = allSessions.some((s) => s.status === 'live');

  useEffect(() => {
    if (!anyLive) return;
    const id = setInterval(() => {
      setLiveTick((n) => n + 1);
    }, 10_000);
    return () => {
      clearInterval(id);
    };
  }, [anyLive]);

  // ── Callbacks ─────────────────────────────────────────────────────────────
  const handleToggleExpand = useCallback((runStartedAt: string) => {
    setExpandedRunAt((prev) => {
      setSelectedSubagentIdx(null);
      return prev === runStartedAt ? null : runStartedAt;
    });
  }, []);

  const handleConfirmClear = useCallback(
    (id: string) => {
      clearIssueLogs.mutate(id);
      clearIssueSubLogs.mutate(id, {
        onSettled: () => {
          setConfirmClearId(null);
        },
      });
    },
    [clearIssueLogs, clearIssueSubLogs],
  );

  // ── Render ────────────────────────────────────────────────────────────────
  return (
    <>
      <PageMeta title="Itervox | Timeline" description="Agent timeline" />

      <div
        data-testid="timeline-filter-bar"
        className="border-theme-line flex items-center gap-2 border-b px-3 py-2"
      >
        <button
          type="button"
          data-testid="timeline-chip-automation"
          aria-pressed={automationOnly}
          aria-disabled={!hasAnyAutomationRun}
          disabled={!hasAnyAutomationRun}
          onClick={() => {
            if (hasAnyAutomationRun) setAutomationOnly(!automationOnly);
          }}
          title={
            hasAnyAutomationRun
              ? 'Show only runs dispatched by an automation rule'
              : 'No automation runs in the current snapshot'
          }
          className={`rounded-full border px-2 py-0.5 font-mono text-[10px] transition-colors ${
            !hasAnyAutomationRun
              ? 'border-theme-line text-theme-muted/60 cursor-not-allowed'
              : automationOnly
                ? 'border-emerald-500/40 bg-emerald-500/15 text-emerald-300'
                : 'border-theme-line text-theme-muted hover:text-theme-text'
          }`}
        >
          automation runs only
        </button>
        <span className="text-theme-muted ml-auto font-mono text-[10px]">
          {allSessions.length} runs
        </span>
      </div>

      <div className="flex" style={{ height: 'calc(100vh - 140px)', minHeight: 500 }}>
        <TimelineSidebar
          issueGroups={issueGroups}
          selectedId={selectedId}
          onSelectIssue={setSelectedId}
          confirmClearId={confirmClearId}
          onRequestClear={setConfirmClearId}
          onCancelClear={() => {
            setConfirmClearId(null);
          }}
          onConfirmClear={handleConfirmClear}
          isClearPending={clearIssueLogs.isPending || clearIssueSubLogs.isPending}
        />

        <TimelineDetailPanel
          selectedId={selectedId}
          selectedGroup={selectedGroup}
          currentAppSessionId={currentAppSessionId}
          anyLive={anyLive}
          logsForSelected={logsForSelected}
          viewStart={viewStart}
          viewEnd={viewEnd}
          expandedRunAt={expandedRunAt}
          selectedSubagentIdx={selectedSubagentIdx}
          onToggleExpand={handleToggleExpand}
          onSelectSubagent={setSelectedSubagentIdx}
          expandedRun={expandedRun}
          selectedSession={selectedSession}
          isSelectedLive={isSelectedLive}
          sublogs={sublogs}
          subagentsForExpanded={subagentsForExpanded}
          validSubagentIdx={validSubagentIdx}
          logPanelSlice={logPanelSlice}
        />
      </div>
    </>
  );
}
