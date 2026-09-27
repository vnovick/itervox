import { useEffect, useId, useMemo, useRef, useState } from 'react';
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

interface Command {
  id: string;
  label: string;
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
      className="max-w-lg"
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
      label: `Go to ${p.label}`,
      hint: p.keys,
      run: () => {
        void navigate(p.path);
      },
    }));
    for (const issue of issues ?? []) {
      out.push({
        id: `issue:${issue.identifier}`,
        label: `Open ${issue.identifier} — ${issue.title}`,
        run: () => {
          setSelected(issue.identifier);
        },
      });
    }
    for (const row of running) {
      out.push({
        id: `pause:${row.identifier}`,
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
      <div className="space-y-3 p-4">
        <p className="text-theme-text text-sm">{pending.confirm.prompt}</p>
        <div className="flex justify-end gap-2">
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setPending(null);
            }}
          >
            Cancel
          </Button>
          <Button
            ref={confirmRef}
            variant="danger"
            size="sm"
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
    <div className="p-2">
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
        className="border-theme-line bg-theme-bg-elevated text-theme-text placeholder:text-theme-muted focus:border-theme-accent w-full rounded-[var(--radius-sm)] border px-3 py-2 text-sm focus:outline-none"
      />
      <ul
        id={listId}
        role="listbox"
        aria-label="Commands"
        className="mt-2 max-h-80 overflow-y-auto"
      >
        {matches.length === 0 && (
          <li role="presentation" className="text-theme-muted px-3 py-2 text-xs">
            No matching commands
          </li>
        )}
        {matches.map((c, i) => (
          // Options are picked with the keyboard via the combobox (arrow keys
          // + Enter); the click is the mouse path to the same action.
          // eslint-disable-next-line jsx-a11y/click-events-have-key-events -- keyboard handled on the combobox (aria-activedescendant pattern)
          <li
            key={c.id}
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
              'flex cursor-pointer items-center justify-between gap-3 rounded px-3 py-2 text-sm',
              i === activeIndex
                ? 'bg-theme-accent-soft text-theme-accent-strong'
                : 'text-theme-text',
            )}
          >
            <span className="truncate">{c.label}</span>
            {c.hint && <kbd className="font-mono text-[11px] text-current">{c.hint}</kbd>}
          </li>
        ))}
      </ul>
    </div>
  );
}
