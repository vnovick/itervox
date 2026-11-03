import { useCallback, useEffect, useId, useMemo, useRef, useState } from 'react';
import { Search } from 'lucide-react';
import { Modal } from '../ui/modal';
import { rankCommands, type Command } from './palette/paletteModel';
import { loadRecents, recordRecent } from './palette/recents';
import { usePaletteCommands } from './palette/usePaletteCommands';
import { PaletteConfirm } from './palette/PaletteConfirm';
import { PaletteList } from './palette/PaletteList';

/**
 * CORE-095 — Mod+K command palette. Built on Modal, so the dialog layer owns
 * Escape, the focus trap and focus restore to the opener. The list is a
 * combobox + listbox (aria-activedescendant); Arrow keys move, Enter runs.
 * Pause, Discard, the dispatch pause and sending a review never run from one
 * keystroke: they open a confirm step inside the palette first.
 *
 * It searches pages, issues, running work, agent profiles, automations,
 * rules, memory, settings fields and sessions, and offers actions (see
 * palette/). The ranking is fuzzy (palette/fuzzy.ts) with recently run
 * commands boosted; a leading `>` `#` `@` `/` narrows the search, and `?`
 * lists those prefixes. With no query it shows the recent commands and the
 * top actions.
 */
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
  const listId = useId();
  const [query, setQuery] = useState('');
  const [active, setActive] = useState(0);
  const [pending, setPending] = useState<Command | null>(null);
  const [recents, setRecents] = useState(loadRecents);
  const confirmRef = useRef<HTMLButtonElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const commands = usePaletteCommands(query);

  const matches = useMemo(() => rankCommands(commands, query, recents), [commands, query, recents]);
  const activeIndex = Math.min(active, Math.max(0, matches.length - 1));
  const optionId = useCallback((i: number) => `${listId}-opt-${String(i)}`, [listId]);

  useEffect(() => {
    if (pending) confirmRef.current?.focus();
    else inputRef.current?.focus();
  }, [pending]);

  const finish = (command: Command) => {
    command.run();
    setRecents(recordRecent(command.id));
    onClose();
  };

  const execute = (command: Command) => {
    if (command.prefill !== undefined) {
      setQuery(command.prefill);
      setActive(0);
      inputRef.current?.focus();
      return;
    }
    if (command.confirm) {
      setPending(command);
      return;
    }
    finish(command);
  };

  if (pending?.confirm) {
    return (
      <PaletteConfirm
        command={pending}
        confirm={pending.confirm}
        confirmRef={confirmRef}
        onCancel={() => {
          setPending(null);
        }}
        onConfirm={() => {
          finish(pending);
        }}
      />
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
          aria-describedby={`${listId}-help`}
          value={query}
          placeholder="Type a command, an issue id, or ? for help…"
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
      <PaletteList
        listId={listId}
        rows={matches}
        activeIndex={activeIndex}
        optionId={optionId}
        onHover={setActive}
        onPick={execute}
      />
      <div
        id={`${listId}-help`}
        className="border-theme-line text-theme-text-secondary flex flex-wrap items-center gap-x-4 gap-y-1 border-t px-5 py-2.5 text-xs"
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
        <span className="flex items-center gap-1.5">
          <kbd className="tag font-mono">&gt; # @ /</kbd> narrow
        </span>
        <span className="flex items-center gap-1.5">
          <kbd className="tag font-mono">?</kbd> help
        </span>
      </div>
    </div>
  );
}
