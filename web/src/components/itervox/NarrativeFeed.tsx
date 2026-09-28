import { useMemo } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useItervoxStore } from '../../store/itervoxStore';
import { useLogStream } from '../../hooks/useLogStream';
import { Terminal } from '../ui/Terminal/Terminal';
import type { LogEntry, LogLevel } from '../ui/Terminal/Terminal';

const MAX_FEED_LINES = 20;

// Map log line text patterns to Terminal log levels
function lineToLevel(line: string): LogLevel {
  const l = line.toLowerCase();
  if (l.includes('error') || l.includes('fail')) return 'error';
  if (l.includes('warn') || l.includes('rate limit') || l.includes('retry')) return 'warn';
  if (l.includes('subagent') || l.includes('spawn')) return 'subagent';
  if (
    l.includes('pull request') ||
    l.includes('pr opened') ||
    l.includes('done') ||
    l.includes('complete')
  )
    return 'action';
  return 'info';
}

export function NarrativeFeed() {
  // CORE-075 — the global log stream lives here, its only reader: it opens
  // when the feed mounts and closes when it unmounts.
  useLogStream();
  const { logs, logSeq } = useItervoxStore(useShallow((s) => ({ logs: s.logs, logSeq: s.logSeq })));

  // CORE-069 — every line carries its store client id, so the Terminal keys
  // (and its unseen-line count) stay stable when the 20-line window advances.
  const entries = useMemo<LogEntry[]>(() => {
    const recent = logs.slice(-MAX_FEED_LINES);
    const firstSeq = logSeq - recent.length + 1;
    return recent.map((line, i) => ({
      ts: firstSeq + i,
      seq: firstSeq + i,
      level: lineToLevel(line),
      message: line,
    }));
  }, [logs, logSeq]);

  return (
    <div
      data-testid="narrative-feed"
      className="border-theme-line bg-theme-panel overflow-hidden rounded-[var(--radius-md)] border"
    >
      <div className="border-theme-line flex items-center justify-between border-b px-4 py-2.5">
        <h3 className="text-theme-text text-sm font-semibold">Recent Events</h3>
        <span className="text-theme-muted font-mono text-xs">last {MAX_FEED_LINES}</span>
      </div>

      {entries.length === 0 ? (
        <div className="text-theme-muted px-4 py-6 text-center text-sm">No events yet</div>
      ) : (
        <Terminal entries={entries} follow showTime={false} />
      )}
    </div>
  );
}
