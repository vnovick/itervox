import { useEffect, useMemo, useRef, useState } from 'react';
import { useItervoxStore } from '../../../store/itervoxStore';
import { useIssueLogs } from '../../../queries/logs';
import { toTermLine } from '../../../utils/logFormatting';
import type { IssueLogEntry } from '../../../types/schemas';
import { usePrefersReducedMotion } from '../../../hooks/usePrefersReducedMotion';

interface AgentLogPanelProps {
  identifier: string;
  logSlice?: IssueLogEntry[];
}

export function AgentLogPanel({ identifier, logSlice }: AgentLogPanelProps) {
  const isLive = useItervoxStore(
    (s) =>
      !!(
        s.snapshot?.running.some((r) => r.identifier === identifier) ||
        s.snapshot?.retrying.some((r) => r.identifier === identifier)
      ),
  );
  // CORE-099 — when the caller already has the entries (Timeline streams the
  // selected issue and passes a slice), subscribing here opened a second
  // stream for the same issue. An empty identifier disables both the stream
  // and the one-shot fetch.
  const { data: liveEntries } = useIssueLogs(logSlice === undefined ? identifier : '', isLive);
  const bottomRef = useRef<HTMLDivElement>(null);
  const containerRef = useRef<HTMLDivElement>(null);
  const followRef = useRef(true);
  const [isFollowing, setIsFollowing] = useState(true);

  // logSlice is provided when viewing a specific subagent's logs; fall back to live entries
  const entries = useMemo(() => logSlice ?? liveEntries, [logSlice, liveEntries]);

  // CORE-024 — scrollIntoView's `behavior` is an explicit argument, so the
  // CSS `scroll-behavior: auto !important` under `prefers-reduced-motion:
  // reduce` (index.css) cannot override it. Honor the preference in JS.
  const prefersReducedMotion = usePrefersReducedMotion();
  const scrollBehavior: ScrollBehavior = prefersReducedMotion ? 'auto' : 'smooth';

  const onScroll = () => {
    if (!containerRef.current) return;
    const { scrollTop, scrollHeight, clientHeight } = containerRef.current;
    followRef.current = scrollHeight - scrollTop - clientHeight < 40;
    setIsFollowing(followRef.current);
  };

  useEffect(() => {
    if (followRef.current) bottomRef.current?.scrollIntoView({ behavior: scrollBehavior });
  }, [entries, scrollBehavior]);

  const scrollToBottom = () => {
    followRef.current = true;
    setIsFollowing(true);
    bottomRef.current?.scrollIntoView({ behavior: scrollBehavior });
  };

  return (
    <div className="flex h-full flex-col overflow-hidden">
      {!isFollowing && (
        <div className="border-theme-line bg-theme-panel flex flex-shrink-0 justify-end border-b px-3 py-1">
          <button
            onClick={scrollToBottom}
            className="text-theme-accent-text text-[10px] font-medium transition-colors hover:opacity-80"
          >
            ▼ Jump to live
          </button>
        </div>
      )}
      <div
        ref={containerRef}
        onScroll={onScroll}
        className="bg-theme-panel-dark flex-1 overflow-y-auto p-3 font-mono text-[12px] leading-[1.6]"
      >
        {entries.length === 0 ? (
          <p className="text-theme-muted italic">No logs yet for {identifier}.</p>
        ) : (
          entries.map((entry) => {
            const line = toTermLine(entry);
            return (
              <div
                key={`${entry.time ?? ''}-${entry.event}-${entry.message.slice(0, 24)}`}
                className="mb-0.5 flex gap-2"
              >
                {line.time && (
                  <span className="text-theme-muted w-[50px] shrink-0">{line.time}</span>
                )}
                <span className="shrink-0" style={{ color: line.prefixColor }}>
                  {line.prefix}
                </span>
                <span
                  className="break-all whitespace-pre-wrap"
                  style={{ color: line.textColor, wordBreak: 'break-word' }}
                >
                  {line.text}
                </span>
              </div>
            );
          })
        )}
        <div ref={bottomRef} />
      </div>
    </div>
  );
}
