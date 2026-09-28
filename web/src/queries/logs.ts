import { useEffect, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import type { IssueLogEntry } from '../types/schemas';
import { IssueLogEntrySchema } from '../types/schemas';
import { z } from 'zod';
import { apiRequest } from '../auth/apiRequest';
import { openAuthedEventStream } from '../auth/authedEventStream';
import { applyLogEntry, applyLogGap, emptyLogStream, type LogStreamState } from './logStream';

export { LIVE_LOG_ENTRY_CAP } from './logStream';

export const logsKey = (identifier: string) => ['logs', identifier] as const;
export const sublogsKey = (identifier: string) => ['sublogs', identifier] as const;
export const logIdentifiersKey = () => ['log-identifiers'] as const;

const EMPTY_LOG_ENTRIES: IssueLogEntry[] = [];
const EMPTY_IDENTIFIERS: string[] = [];

// Each live stream instance (one per effect run) gets a distinct number, so
// its first frames replace whatever an earlier instance left in state.
let nextStreamInstance = 0;

/**
 * Subscribes to one live log SSE stream and keeps its lines across reconnects
 * (CORE-027; see logStream.ts for the resume/de-dupe/gap rules). `event` is
 * the SSE event name carrying log lines ('log' or 'sublog').
 */
function useLiveLogStream(path: string, event: string, identifier: string, isLive: boolean) {
  const [stream, setStream] = useState<LogStreamState>(() => emptyLogStream(identifier));
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState(false);

  useEffect(() => {
    if (!isLive || !identifier) return;
    nextStreamInstance += 1;
    const instance = nextStreamInstance;

    // No reset here (react-hooks/set-state-in-effect): the hook's output is
    // keyed by identifier, so a different identifier shows nothing until its
    // own lines arrive, and this instance's first frame replaces any lines an
    // earlier instance left behind.
    const close = openAuthedEventStream(path, {
      // onOpen fires on every (re)connection. It must NOT clear the lines:
      // a retry resends Last-Event-ID and the server resumes after it, so
      // the lines already shown are not replayed (CORE-027).
      onOpen: () => {
        setLoading(false);
        setError(false);
      },
      onMessage: (msg) => {
        if (msg.event === 'gap') {
          setStream((prev) => applyLogGap(prev, identifier, instance, msg.id));
          return;
        }
        if (msg.event !== event) return; // keepalives, error frames, …
        try {
          const entry = IssueLogEntrySchema.parse(JSON.parse(msg.data) as unknown);
          setStream((prev) => applyLogEntry(prev, identifier, instance, msg.id, entry));
        } catch {
          // malformed event — skip
        }
      },
      onDisconnect: () => {
        setError(true);
        setLoading(false);
      },
    });

    return () => {
      close();
    };
  }, [path, event, identifier, isLive]);

  const data = stream.identifier === identifier ? stream.entries : EMPTY_LOG_ENTRIES;
  return { data, isLoading: loading, isError: error };
}

async function fetchLogIdentifiers(): Promise<string[]> {
  const { data } = await apiRequest('/api/v1/logs/identifiers', { op: 'fetch log identifiers' });
  return z.array(z.string()).parse(data);
}

async function fetchIssueLogs(identifier: string): Promise<IssueLogEntry[]> {
  const { data } = await apiRequest(`/api/v1/issues/${encodeURIComponent(identifier)}/logs`, {
    op: 'fetch logs',
  });
  return z.array(IssueLogEntrySchema).parse(data);
}

async function fetchSubLogs(identifier: string): Promise<IssueLogEntry[]> {
  const { data } = await apiRequest(`/api/v1/issues/${encodeURIComponent(identifier)}/sublogs`, {
    op: 'fetch sublogs',
  });
  return z.array(IssueLogEntrySchema).parse(data);
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
  const live = useLiveLogStream(
    `/api/v1/issues/${encodeURIComponent(identifier)}/log-stream`,
    'log',
    identifier,
    isLive,
  );

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

  if (isLive) return live;
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
  const live = useLiveLogStream(
    `/api/v1/issues/${encodeURIComponent(identifier)}/sublog-stream`,
    'sublog',
    identifier,
    isLive,
  );

  const { data, isLoading, isError } = useQuery({
    queryKey: sublogsKey(identifier),
    queryFn: () => fetchSubLogs(identifier),
    enabled: !!identifier && !isLive,
    staleTime: Infinity,
  });

  if (isLive) return live;
  return { data, isLoading, isError };
}

/**
 * Returns the list of issue identifiers that have log data on the server
 * (either in-memory or persisted to disk). Use this for the Logs sidebar
 * instead of the full issue list from the tracker.
 */
export function useLogIdentifiers(): { data: string[]; settled: boolean } {
  const {
    data = EMPTY_IDENTIFIERS,
    isSuccess,
    isError,
  } = useQuery({
    queryKey: logIdentifiersKey(),
    queryFn: fetchLogIdentifiers,
    staleTime: 10_000,
  });
  // settled: the first fetch has finished either way (CORE-085 waits for it
  // before treating a /logs/:identifier deep link as unknown).
  return { data, settled: isSuccess || isError };
}
