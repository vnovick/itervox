import { useMemo, useState } from 'react';
import { useSkillsInventory } from '../../../queries/skills';
import { fieldSurfaceCls, helperTextCls, inputCls } from '../formStyles';
import type { SupportedBackend } from '../profileCommands';
import {
  backendItems,
  describeCounts,
  itemCounts,
  referenceText,
  type BackendItem,
} from './backendInventory';
import { backendLabel } from './profileBadges';

interface ProfileReferencePickerProps {
  /** The backend the profile runs on; only what it can use is listed. */
  backend: SupportedBackend;
  /** Inserts a reference into the profile's instructions. */
  onInsert: (reference: string) => void;
}

const KIND_LABEL: Record<BackendItem['kind'], string> = {
  skill: 'skill',
  subagent: 'subagent',
  prompt: 'prompt',
};

/**
 * Lists the skills, subagents (Claude) and custom prompts (Codex) the
 * profile's backend can use (#123) and inserts a reference the doctor check
 * recognises into the profile's instructions.
 */
export function ProfileReferencePicker({ backend, onInsert }: ProfileReferencePickerProps) {
  const { data: inventory } = useSkillsInventory();
  const [query, setQuery] = useState('');
  const items = useMemo(() => backendItems(inventory, backend), [inventory, backend]);
  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    return q ? items.filter((item) => item.name.toLowerCase().includes(q)) : items;
  }, [items, query]);
  const title = `Skills and agents for ${backendLabel(backend)}`;

  if (!inventory) return null;

  return (
    <section aria-label={title} className={fieldSurfaceCls}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <p className="field-label mb-0">{title}</p>
          <p className="field-help mt-0.5">
            {describeCounts(itemCounts(items))}. Click one to add a reference to INSTRUCTIONS.md.
          </p>
        </div>
        {items.length > 8 && (
          <input
            type="search"
            aria-label="Filter skills and agents"
            value={query}
            onChange={(e) => {
              setQuery(e.target.value);
            }}
            placeholder="Filter"
            className={`${inputCls} max-w-[200px] text-[13px]`}
          />
        )}
      </div>
      {visible.length > 0 ? (
        <ul className="flex max-h-40 flex-wrap gap-1 overflow-y-auto">
          {visible.map((item) => (
            <li key={`${item.kind}:${item.name}`}>
              <button
                type="button"
                title={item.description}
                aria-label={`Insert ${KIND_LABEL[item.kind]} ${item.name}`}
                onClick={() => {
                  onInsert(referenceText(item));
                }}
                className="pill pill-neutral hover:text-theme-text px-2.5 py-0.5 font-mono text-xs"
              >
                {item.name}
                <span className="text-theme-muted ml-1 font-sans">{KIND_LABEL[item.kind]}</span>
              </button>
            </li>
          ))}
        </ul>
      ) : (
        <p className={helperTextCls}>
          {items.length === 0
            ? `No skills found for ${backendLabel(backend)} on this machine.`
            : 'Nothing matches the filter.'}
        </p>
      )}
    </section>
  );
}
