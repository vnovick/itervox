import { Fragment, useEffect, useId, useMemo, useRef, useState } from 'react';
import { Search } from 'lucide-react';
import { useNavigate } from 'react-router';
import { Modal } from '../ui/modal';
import { Button } from '../ui/button';
import { cn } from '../ui/cn';
import { useItervoxStore } from '../../store/itervoxStore';
import { useCancelIssue, useIssues, useTerminateIssue } from '../../queries/issues';
import { EMPTY_RUNNING } from '../../utils/constants';
import { PAGES } from './commandPalettePages';

/**
 * CORE-095 — Mod+K command palette: go to a page, open an issue, or pause /
 * discard a running issue. Built on Modal, so the dialog layer owns Escape,
 * the focus trap and focus restore to the opener. The list is a combobox +
 * listbox (aria-activedescendant); Arrow keys move, Enter runs. Pause and
 * Discard never run from one keystroke: they open a confirm step inside the
 * palette first.
 */

type CommandGroup = 'pages' | 'issues' | 'runs';

const GROUP_LABEL: Record<CommandGroup, string> = {
  pages: 'Go to',
  issues: 'Issues',
  runs: 'Running',
};

interface Command {
  id: string;
  group: CommandGroup;
  label: string;
  /** A leading identifier, shown in the mono face. */
  tag?: string;
  /** Destructive commands read in the danger tone. */
  danger?: boolean;
  hint?: string;
  run: () => void;
  confirm?: { prompt: string; confirmLabel: string };
}

export function CommandPalette({ isOpen, onClose }: { isOpen: boolean; onClose: () => void }) {
  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      showCloseButton={false}
      ariaLabel="Command palette"
      className="max-w-[560px] overflow-hidden"
    >
      {isOpen && <PaletteBody onClose={onClose} />}
    </Modal>
  );
}

function PaletteBody({ onClose }: { onClose: () => void }) {
  const navigate = useNavigate();
  const listId = useId();
  const [query, setQuery] = useState('');
  const [active, setActive] = useState(0);
  const [pending, setPending] = useState<Command | null>(null);
  const confirmRef = useRef<HTMLButtonElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const { data: issues } = useIssues();
  const running = useItervoxStore((s) => s.snapshot?.running ?? EMPTY_RUNNING);
  const setSelected = useItervoxStore((s) => s.setSelectedIdentifier);
  const pause = useCancelIssue();
  const discard = useTerminateIssue();

  const commands = useMemo<Command[]>(() => {
    const out: Command[] = PAGES.map((p) => ({
      id: `page:${p.path}`,
      group: 'pages',
      label: `Go to ${p.label}`,
      hint: p.keys,
      run: () => {
        void navigate(p.path);
      },
    }));
    for (const issue of issues ?? []) {
      out.push({
        id: `issue:${issue.identifier}`,
        group: 'issues',
        tag: issue.identifier,
        label: `Open ${issue.identifier} — ${issue.title}`,
        run: () => {
          setSelected(issue.identifier);
        },
      });
    }
    for (const row of running) {
      out.push({
        id: `pause:${row.identifier}`,
        group: 'runs',
        label: `Pause ${row.identifier}`,
        run: () => {
          pause.mutate(row.identifier);
        },
        confirm: {
          prompt: `Pause ${row.identifier}? The running agent is stopped.`,
          confirmLabel: `Yes, pause ${row.identifier}`,
        },
      });
      out.push({
        id: `discard:${row.identifier}`,
        group: 'runs',
        danger: true,
        label: `Discard ${row.identifier}`,
        run: () => {
          discard.mutate(row.identifier);
        },
        confirm: {
          prompt: `Discard ${row.identifier}? The run stops and the issue moves back to the backlog.`,
          confirmLabel: `Yes, discard ${row.identifier}`,
        },
      });
    }
    return out;
  }, [issues, running, navigate, setSelected, pause, discard]);

  const q = query.trim().toLowerCase();
  const matches = q
    ? commands.filter((c) => q.split(/\s+/).every((word) => c.label.toLowerCase().includes(word)))
    : commands;
  const activeIndex = Math.min(active, Math.max(0, matches.length - 1));
  const optionId = (i: number) => `${listId}-opt-${String(i)}`;

  useEffect(() => {
    if (pending) confirmRef.current?.focus();
    else inputRef.current?.focus();
  }, [pending]);

  const execute = (command: Command) => {
    if (command.confirm) {
      setPending(command);
      return;
    }
    command.run();
    onClose();
  };

  if (pending?.confirm) {
    return (
      <div className="space-y-4 px-5 py-5">
        <p className="text-theme-text text-[15px] leading-relaxed">{pending.confirm.prompt}</p>
        <div className="flex justify-end gap-2">
          <Button
            variant="ghost"
            size="lg"
            onClick={() => {
              setPending(null);
            }}
          >
            Cancel
          </Button>
          <Button
            ref={confirmRef}
            variant="destructive"
            size="lg"
            onClick={() => {
              pending.run();
              onClose();
            }}
          >
            {pending.confirm.confirmLabel}
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div>
      <div className="border-theme-line flex items-center gap-3 border-b px-5 py-3.5">
        <Search aria-hidden="true" className="text-theme-text-secondary h-4 w-4 flex-shrink-0" />
        <input
          ref={inputRef}
          role="combobox"
          aria-label="Search commands and issues"
          aria-expanded="true"
          aria-controls={listId}
          aria-activedescendant={matches.length > 0 ? optionId(activeIndex) : undefined}
          aria-autocomplete="list"
          value={query}
          placeholder="Type a command or an issue id…"
          onChange={(e) => {
            setQuery(e.target.value);
            setActive(0);
          }}
          onKeyDown={(e) => {
            if (e.key === 'ArrowDown') {
              e.preventDefault();
              setActive((i) => Math.min(i + 1, matches.length - 1));
            } else if (e.key === 'ArrowUp') {
              e.preventDefault();
              setActive((i) => Math.max(i - 1, 0));
            } else if (e.key === 'Enter') {
              e.preventDefault();
              const hit = matches[activeIndex] as Command | undefined;
              if (hit) execute(hit);
            }
          }}
          className="text-theme-text placeholder:text-theme-muted min-w-0 flex-1 rounded-md bg-transparent text-base"
        />
        <kbd aria-hidden="true" className="tag font-mono">
          esc
        </kbd>
      </div>
      <ul
        id={listId}
        role="listbox"
        aria-label="Commands"
        className="max-h-[420px] overflow-y-auto px-2 py-2"
      >
        {matches.length === 0 && (
          <li role="presentation" className="text-theme-muted px-3 py-2 text-xs">
            No matching commands
          </li>
        )}
        {matches.map((c, i) => (
          <Fragment key={c.id}>
            {(i === 0 || matches[i - 1].group !== c.group) && (
              <li role="presentation" aria-hidden="true" className="eyebrow px-3 pt-2.5 pb-1.5">
                {GROUP_LABEL[c.group]}
              </li>
            )}
            {/* Options are picked with the keyboard via the combobox (arrow
                keys + Enter); the click is the mouse path to the same action. */}
            {/* eslint-disable-next-line jsx-a11y/click-events-have-key-events -- keyboard handled on the combobox (aria-activedescendant pattern) */}
            <li
              id={optionId(i)}
              role="option"
              aria-selected={i === activeIndex}
              onMouseEnter={() => {
                setActive(i);
              }}
              onClick={() => {
                execute(c);
              }}
              className={cn(
                'flex min-h-11 cursor-pointer items-center gap-3 rounded-[var(--radius-sm)] px-3 text-sm',
                i === activeIndex
                  ? 'bg-theme-accent-soft text-theme-accent-strong font-medium'
                  : c.danger
                    ? 'text-theme-danger-text'
                    : 'text-theme-text',
              )}
            >
              {c.tag && (
                <span
                  className={cn(
                    'flex-shrink-0 font-mono text-xs',
                    i === activeIndex ? 'font-semibold' : 'text-theme-text-secondary',
                  )}
                >
                  {c.tag}
                </span>
              )}
              <span className="min-w-0 flex-1 truncate">{c.label}</span>
              {c.hint && (
                <kbd className="text-theme-text-secondary font-mono text-[11px]">{c.hint}</kbd>
              )}
              {i === activeIndex && !c.hint && (
                <kbd aria-hidden="true" className="font-mono text-[11px]">
                  ↵
                </kbd>
              )}
            </li>
          </Fragment>
        ))}
      </ul>
      <div
        aria-hidden="true"
        className="border-theme-line text-theme-text-secondary flex items-center gap-4 border-t px-5 py-2.5 text-xs"
      >
        <span className="flex items-center gap-1.5">
          <kbd className="tag font-mono">↑↓</kbd> move
        </span>
        <span className="flex items-center gap-1.5">
          <kbd className="tag font-mono">↵</kbd> run
        </span>
        <span className="flex items-center gap-1.5">
          <kbd className="tag font-mono">esc</kbd> close
        </span>
      </div>
    </div>
  );
}
