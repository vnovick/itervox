import { useId, useMemo, useRef, useState, type KeyboardEvent } from 'react';
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

interface OpenState {
  ctx: CompletionContext;
  items: Completion[];
  pos: CaretPosition;
}

/**
 * The prompt textarea with autocomplete (#87): `/` or `@` suggests skills and
 * subagents from the inventory, `{{` suggests Liquid variables, and `|` inside
 * `{{ … }}` suggests filters. Arrow keys move, Enter or Tab inserts, Escape
 * closes.
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
    setOpen({ ctx, items, pos: caretPosition(ref.current, caret) });
    setActive(0);
  };

  const choose = (item: Completion) => {
    const el = ref.current;
    if (!el || !open) return;
    const next = applyCompletion(value, el.selectionStart, open.ctx, item);
    onChange(next.text);
    setOpen(null);
    requestAnimationFrame(() => {
      el.focus();
      el.setSelectionRange(next.caret, next.caret);
    });
  };

  const onKeyDown = (event: KeyboardEvent<HTMLTextAreaElement>) => {
    if (!open) return;
    const n = open.items.length;
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      setActive((i) => (i + (event.key === 'ArrowDown' ? 1 : n - 1)) % n);
    } else if (event.key === 'Enter' || event.key === 'Tab') {
      event.preventDefault();
      choose(open.items[active]);
    } else if (event.key === 'Escape') {
      event.preventDefault();
      event.stopPropagation(); // close the list, not the surrounding modal
      setOpen(null);
    }
  };

  const optionId = (i: number) => `${listId}-option-${String(i)}`;

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
          className="border-theme-line bg-theme-panel absolute z-20 max-h-64 w-80 max-w-full overflow-y-auto rounded-[var(--radius-sm)] border py-1 shadow-lg"
          style={{ top: open.pos.top + open.pos.height + 4, left: Math.max(0, open.pos.left) }}
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
