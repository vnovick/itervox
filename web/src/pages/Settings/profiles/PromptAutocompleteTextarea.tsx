import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { useSkillsInventory } from '../../../queries/skills';
import { caretPosition, type CaretPosition } from './caretPosition';
import {
  applyCompletion,
  candidatesFor,
  completionContext,
  matchCompletions,
  referenceCompletions,
  type Completion,
  type CompletionContext,
} from './promptCompletions';

interface PromptAutocompleteTextareaProps {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  className?: string;
  label: string;
  /** Liquid variables offered after `{{`. */
  variables: readonly Completion[];
}

const KIND_LABEL: Record<Completion['kind'], string> = {
  skill: 'skill',
  subagent: 'subagent',
  variable: 'variable',
  filter: 'filter',
};

/** Keys the open list handles itself. */
const LIST_KEYS = new Set(['ArrowUp', 'ArrowDown', 'Enter', 'Escape']);

interface OpenState {
  ctx: CompletionContext;
  items: Completion[];
  pos: CaretPosition;
}

/**
 * The prompt textarea with autocomplete (#87): `/` or `@` suggests skills and
 * subagents from the inventory, `{{` suggests Liquid variables, and `|` inside
 * `{{ … }}` suggests filters. Arrow keys move, Enter inserts, Escape closes.
 * Tab is left to focus navigation (the dialog's focus trap).
 */
export function PromptAutocompleteTextarea({
  value,
  onChange,
  placeholder,
  className,
  label,
  variables,
}: PromptAutocompleteTextareaProps) {
  const ref = useRef<HTMLTextAreaElement>(null);
  const listId = useId();
  const { data: inventory } = useSkillsInventory();
  const references = useMemo(() => referenceCompletions(inventory), [inventory]);
  const [open, setOpen] = useState<OpenState | null>(null);
  const [active, setActive] = useState(0);

  const refresh = (text: string, caret: number) => {
    const ctx = completionContext(text, caret);
    const items = ctx ? matchCompletions(candidatesFor(ctx, references, variables), ctx.query) : [];
    if (!ctx || items.length === 0 || !ref.current) {
      setOpen(null);
      return;
    }
    const pos = caretPosition(ref.current, caret);
    // Keep the popover (w-80) inside the textarea's width.
    pos.left = Math.max(0, Math.min(pos.left, ref.current.clientWidth - 320));
    setOpen({ ctx, items, pos });
    setActive(0);
  };

  // Inserts item at the caret. The context is worked out again from the
  // caret now: if it moved since the list opened (arrow keys, Home, End),
  // the list is stale and nothing is inserted.
  const choose = (item: Completion): boolean => {
    const el = ref.current;
    if (!el || !open) return false;
    const ctx = completionContext(value, el.selectionStart);
    if (!ctx || ctx.kind !== open.ctx.kind || ctx.start !== open.ctx.start) {
      setOpen(null);
      return false;
    }
    const next = applyCompletion(value, el.selectionStart, ctx, item, el.selectionEnd);
    onChange(next.text);
    setOpen(null);
    requestAnimationFrame(() => {
      el.focus();
      el.setSelectionRange(next.caret, next.caret);
    });
    return true;
  };

  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    // Keys that finish an IME composition belong to the IME.
    if (!open || event.nativeEvent.isComposing) return;
    const n = open.items.length;
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      setActive((i) => (i + (event.key === 'ArrowDown' ? 1 : n - 1)) % n);
    } else if (event.key === 'Enter') {
      // A stale list lets Enter type its newline.
      if (choose(open.items[active])) event.preventDefault();
    } else if (event.key === 'Escape') {
      event.preventDefault();
      event.stopPropagation(); // close the list, not the surrounding modal
      setOpen(null);
    }
  };

  const optionId = (i: number) => `${listId}-option-${String(i)}`;

  // Keep the highlighted option visible as the arrow keys move.
  useEffect(() => {
    if (!open) return;
    const option: Partial<HTMLElement> | null = document.getElementById(optionId(active));
    option?.scrollIntoView?.({ block: 'nearest' }); // absent in jsdom
  });

  return (
    <div className="relative">
      <textarea
        ref={ref}
        value={value}
        aria-label={label}
        aria-autocomplete="list"
        aria-haspopup="listbox"
        aria-controls={open ? listId : undefined}
        aria-activedescendant={open ? optionId(active) : undefined}
        onChange={(event) => {
          onChange(event.target.value);
          refresh(event.target.value, event.target.selectionStart);
        }}
        onKeyDown={onKeyDown}
        onKeyUp={(event) => {
          // Caret moves without an edit (ArrowLeft/Right, Home, End): check
          // the context again, or the list would insert at a stale place.
          if (open && !LIST_KEYS.has(event.key)) {
            refresh(event.currentTarget.value, event.currentTarget.selectionStart);
          }
        }}
        onScroll={(event) => {
          // Follow the caret (typing can scroll a long prompt).
          if (open) refresh(event.currentTarget.value, event.currentTarget.selectionStart);
        }}
        onClick={(event) => {
          refresh(event.currentTarget.value, event.currentTarget.selectionStart);
        }}
        onBlur={() => {
          setOpen(null);
        }}
        placeholder={placeholder}
        className={className}
      />
      <span role="status" aria-live="polite" className="sr-only">
        {open
          ? `${String(open.items.length)} suggestion${open.items.length === 1 ? '' : 's'}; arrow keys to choose, Enter to insert, Escape to close`
          : ''}
      </span>
      {open && (
        <ul
          id={listId}
          role="listbox"
          aria-label={`${label} suggestions`}
          data-testid="prompt-suggestions"
          onMouseDown={(event) => {
            event.preventDefault(); // scrolling the list keeps the textarea focused
          }}
          className="border-theme-line bg-theme-panel absolute z-20 max-h-64 w-80 max-w-full overflow-y-auto rounded-[var(--radius-sm)] border py-1 shadow-lg"
          style={{
            top: open.pos.top + open.pos.height + 4,
            left: open.pos.left,
          }}
        >
          {open.items.map((item, i) => (
            <li
              key={`${item.kind}:${item.label}`}
              id={optionId(i)}
              role="option"
              aria-selected={i === active}
              onMouseDown={(event) => {
                event.preventDefault(); // keep the textarea focused
                choose(item);
              }}
              onMouseEnter={() => {
                setActive(i);
              }}
              className={`cursor-pointer px-3 py-1.5 text-[12px] ${
                i === active ? 'bg-theme-accent-soft' : ''
              }`}
            >
              <div className="flex items-baseline justify-between gap-2">
                <span className="text-theme-text font-mono">{item.label}</span>
                <span className="text-theme-muted text-[10px]">
                  {KIND_LABEL[item.kind]}
                  {item.source ? ` · ${item.source}` : ''}
                </span>
              </div>
              {item.detail && (
                <div className="text-theme-text-secondary truncate">{item.detail}</div>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
