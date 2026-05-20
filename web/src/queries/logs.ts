import { useEffect, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import type { IssueLogEntry } from '../types/schemas';
import { IssueLogEntrySchema } from '../types/schemas';
import { z } from 'zod';
import { authedFetch } from '../auth/authedFetch';
import { openAuthedEventStream } from '../auth/authedEventStream';

export const LIVE_LOG_ENTRY_CAP = 500;

export const logsKey = (identifier: string) => ['logs', identifier] as const;
export const sublogsKey = (identifier: string) => ['sublogs', identifier] as const;
export const logIdentifiersKey = () => ['log-identifiers'] as const;

function appendCappedLogEntry(prev: IssueLogEntry[], entry: IssueLogEntry): IssueLogEntry[] {
  if (prev.length >= LIVE_LOG_ENTRY_CAP) {
    return [...prev.slice(prev.length - LIVE_LOG_ENTRY_CAP + 1), entry];
  }
  return [...prev, entry];
}

async function fetchLogIdentifiers(): Promise<string[]> {
  const res = await authedFetch('/api/v1/logs/identifiers');
  if (!res.ok) throw new Error(`fetch log identifiers failed: ${String(res.status)}`);
  return z.array(z.string()).parse(await res.json());
}

async function fetchIssueLogs(identifier: string): Promise<IssueLogEntry[]> {
  const res = await authedFetch(`/api/v1/issues/${encodeURIComponent(identifier)}/logs`);
  if (!res.ok) throw new Error(`fetch logs failed: ${String(res.status)}`);
  return z.array(IssueLogEntrySchema).parse(await res.json());
}

async function fetchSubLogs(identifier: string): Promise<IssueLogEntry[]> {
  const res = await authedFetch(`/api/v1/issues/${encodeURIComponent(identifier)}/sublogs`);
  if (!res.ok) throw new Error(`fetch sublogs failed: ${String(res.status)}`);
  return z.array(IssueLogEntrySchema).parse(await res.json());
}

/**
 * Fetches issue log entries.
 *
 * - isLive=true: uses SSE (/api/v1/issues/{id}/log-stream) — push-based, no polling.
 * - isLive=false: one-shot TanStack Query fetch with 30s stale time.
 *
 * API is identical for all callers regardless of mode.
 */
export function useIssueLogs(identifier: string, isLive: boolean) {
  // SSE state — always declared (rules of hooks), activated only when isLive
  const [sseData, setSseData] = useState<IssueLogEntry[]>([]);
  const [sseLoading, setSseLoading] = useState(false);
  const [sseError, setSseError] = useState(false);

  useEffect(() => {
    if (!isLive || !identifier) return;

    // Note: state resets (clearing data, setting loading=true) happen inside
    // onOpen below rather than in this effect body. That avoids the
    // react-hooks/set-state-in-effect lint rule and also means we don't paint
    // an empty "loading…" state if the new connection opens within one frame.
    // The trade-off: between identifier change and first onOpen, the UI may
    // briefly show stale lines from the previous identifier (typically <100ms).

    const close = openAuthedEventStream(
      `/api/v1/issues/${encodeURIComponent(identifier)}/log-stream`,
      {
        // onOpen fires on every (re)connection. Clear stale lines so the
        // server's replayed initial batch doesn't duplicate what we already
        // rendered before the disconnect, and reset loading/error state.
        onOpen: () => {
          setSseData([]);
          setSseLoading(false);
          setSseError(false);
        },
        onMessage: (msg) => {
          // Only handle the 'log' named event, ignore keepalives etc.
          if (msg.event !== 'log') return;
          try {
            const entry = IssueLogEntrySchema.parse(JSON.parse(msg.data) as unknown);
            setSseData((prev) => appendCappedLogEntry(prev, entry));
          } catch {
            // malformed event — skip
          }
        },
        onDisconnect: () => {
          setSseError(true);
          setSseLoading(false);
        },
      },
    );

    return () => {
      close();
    };
  }, [identifier, isLive]);

  // One-shot query — disabled when isLive to avoid redundant HTTP fetches
  const {
    data: queryData,
    isLoading: queryLoading,
    isError: queryError,
  } = useQuery({
    queryKey: logsKey(identifier),
    queryFn: () => fetchIssueLogs(identifier),
    enabled: !!identifier && !isLive,
    staleTime: 15_000,
  });

  if (isLive) {
    return { data: sseData, isLoading: sseLoading, isError: sseError };
  }
  return { data: queryData ?? [], isLoading: queryLoading, isError: queryError };
}

/**
 * Fetches full session logs written by Claude Code to CLAUDE_CODE_LOG_DIR.
 * Covers all subagents, not just the top-level orchestrator log buffer.
 *
 * - isLive=true: uses SSE (/api/v1/issues/{id}/sublog-stream) — push-based.
 * - isLive=false: one-shot TanStack Query fetch with infinite stale time.
 */
export function useSubagentLogs(identifier: string, isLive: boolean) {
  const [sseData, setSseData] = useState<IssueLogEntry[]>([]);
  const [sseLoading, setSseLoading] = useState(false);
  const [sseError, setSseError] = useState(false);

  useEffect(() => {
    if (!isLive || !identifier) return;

    const close = openAuthedEventStream(
      `/api/v1/issues/${encodeURIComponent(identifier)}/sublog-stream`,
      {
        onOpen: () => {
          setSseData([]);
          setSseLoading(false);
          setSseError(false);
        },
        onMessage: (msg) => {
          if (msg.event !== 'sublog') return;
          try {
            const entry = IssueLogEntrySchema.parse(JSON.parse(msg.data) as unknown);
            setSseData((prev) => appendCappedLogEntry(prev, entry));
          } catch {
            // malformed event — skip
          }
        },
        onDisconnect: () => {
          setSseError(true);
          setSseLoading(false);
        },
      },
    );

    return () => {
      close();
    };
  }, [identifier, isLive]);

  const { data, isLoading, isError } = useQuery({
    queryKey: sublogsKey(identifier),
    queryFn: () => fetchSubLogs(identifier),
    enabled: !!identifier && !isLive,
    staleTime: Infinity,
  });

  if (isLive) {
    return { data: sseData, isLoading: sseLoading, isError: sseError };
  }
  return { data, isLoading, isError };
}

/**
 * Returns the list of issue identifiers that have log data on the server
 * (either in-memory or persisted to disk). Use this for the Logs sidebar
 * instead of the full issue list from the tracker.
 */
export function useLogIdentifiers() {
  const { data = [] } = useQuery({
    queryKey: logIdentifiersKey(),
    queryFn: fetchLogIdentifiers,
    staleTime: 10_000,
  });
  return data;
}
