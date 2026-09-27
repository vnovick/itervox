import { useEffect, useRef, useState, startTransition } from 'react';
import type { IssueLogEntry } from '../../types/schemas';
import { useIssueLogs } from '../../queries/logs';
import { Terminal } from '../ui/Terminal/Terminal';
import { issueLogToTerminal } from '../../utils/logFormatting';

interface LogSection {
  label: string;
  isSubagent: boolean;
  entries: IssueLogEntry[];
}

function buildSections(entries: IssueLogEntry[]): LogSection[] {
  const sections: LogSection[] = [{ label: 'Main', isSubagent: false, entries: [] }];
  for (const entry of entries) {
    if (entry.event === 'subagent') {
      sections.push({
        label: entry.message.slice(0, 45),
        isSubagent: true,
        entries: [entry],
      });
    } else {
      sections[sections.length - 1].entries.push(entry);
    }
  }
  return sections;
}

interface SessionAccordionProps {
  identifier: string;
  workerHost: string | undefined;
  sessionId: string | undefined;
}

export function SessionAccordion({ identifier, workerHost, sessionId }: SessionAccordionProps) {
  const [selectedIdx, setSelectedIdx] = useState(0);
  const prevSectionCountRef = useRef(0);
  const { data: logs } = useIssueLogs(identifier, true);
  const sections = buildSections(logs);

  useEffect(() => {
    const prev = prevSectionCountRef.current;
    if (sections.length > prev && selectedIdx === prev - 1) {
      startTransition(() => {
        setSelectedIdx(sections.length - 1);
      });
    }
    prevSectionCountRef.current = sections.length;
  }, [sections.length, selectedIdx]);

  const active = sections[selectedIdx] ?? sections[0];
  const termEntries = active.entries.map(issueLogToTerminal);

  return (
    <div className="border-theme-line border-t" style={{ background: 'var(--bg)' }}>
      <div className="border-theme-line text-theme-muted flex items-center gap-6 border-b px-4 py-2 font-mono text-xs">
        <span>
          Worker: <span className="text-theme-text-secondary">{workerHost ?? 'local'}</span>
        </span>
        {sessionId && (
          <span title={sessionId}>
            Session: <span className="text-theme-text-secondary">{sessionId.slice(0, 8)}</span>
          </span>
        )}
      </div>

      <div className="flex" style={{ height: 240 }}>
        <div className="border-theme-line flex w-44 flex-shrink-0 flex-col border-r">
          <div className="border-theme-line text-theme-muted border-b px-3 py-2 text-[10px] font-semibold tracking-wider uppercase">
            {sections.length > 1
              ? `${String(sections.length - 1)} subagent${sections.length > 2 ? 's' : ''}`
              : 'Logs'}
          </div>
          <div className="flex-1 overflow-y-auto">
            {sections.map((sec, i) => (
              <button
                key={sec.label}
                onClick={() => {
                  setSelectedIdx(i);
                }}
                className="terminal-tab flex w-full items-center gap-2 border-b px-3 py-2 text-left text-xs transition-colors"
                style={{
                  borderColor: 'var(--line)',
                  background: i === selectedIdx ? 'var(--accent-soft)' : 'transparent',
                  color: i === selectedIdx ? 'var(--accent-strong)' : 'var(--text-secondary)',
                }}
              >
                <span style={{ color: sec.isSubagent ? 'var(--purple)' : 'var(--muted)' }}>
                  {sec.isSubagent ? '↗' : '◈'}
                </span>
                <span className="flex-1 truncate font-mono">{sec.label}</span>
                <span className="text-theme-muted">{sec.entries.length}</span>
              </button>
            ))}
          </div>
        </div>

        <Terminal
          entries={termEntries}
          follow
          showTime={false}
          className="h-full flex-1"
          resetKey={`${identifier}|${String(selectedIdx)}`}
        />
      </div>
    </div>
  );
}
