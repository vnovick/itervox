import { Fragment, useId } from 'react';
import type { FilterGroup } from '../../../types/schemas';
import {
  FILTER_CONDITIONS,
  conditionMeta,
  groupConditions,
  type FilterConditionKey,
} from '../../../utils/filterGroups';
import { FilterConditionInput } from './FilterConditionInput';

// #198 — one AND group of the tracker filter: condition rows joined by AND,
// an "add condition" picker limited to the conditions the tracker supports.

interface Props {
  group: FilterGroup;
  index: number;
  /** The conditions the tracker supports (GET /api/v1/tracker/scope). */
  supported: readonly string[];
  onChange: (group: FilterGroup) => void;
  onRemove: () => void;
}

function emptyValue(key: FilterConditionKey): FilterGroup[FilterConditionKey] {
  const meta = conditionMeta(key);
  return meta.kind === 'single' ? '' : [];
}

export function FilterGroupEditor({ group, index, supported, onChange, onRemove }: Props) {
  const baseId = useId();
  const set = groupConditions(group);
  const addable = FILTER_CONDITIONS.filter(
    (c) => supported.includes(c.key) && !set.includes(c.key),
  );
  const title = `Group ${String(index + 1)}`;

  const update = (key: FilterConditionKey, value: FilterGroup[FilterConditionKey]) => {
    onChange({ ...group, [key]: value });
  };
  const remove = (key: FilterConditionKey) => {
    onChange(Object.fromEntries(Object.entries(group).filter(([k]) => k !== key)) as FilterGroup);
  };

  return (
    <fieldset
      aria-label={title}
      className="border-theme-line bg-theme-bg space-y-3 rounded-[var(--radius-md)] border p-4"
    >
      <div className="flex items-center justify-between gap-2">
        <legend className="text-theme-text text-sm font-semibold">
          {title}
          <span className="text-theme-muted ml-2 text-xs font-normal">
            every condition must match
          </span>
        </legend>
        <button
          type="button"
          onClick={onRemove}
          className="text-theme-text-secondary text-xs hover:opacity-70"
          aria-label={`Remove ${title}`}
        >
          Remove group
        </button>
      </div>

      {set.length === 0 && (
        <p className="text-theme-danger-text text-xs">
          Add at least one condition — an empty group would match every issue.
        </p>
      )}

      {set.map((key, i) => {
        const meta = conditionMeta(key);
        const inputId = `${baseId}-${key}`;
        return (
          <Fragment key={key}>
            {i > 0 && (
              <div className="text-theme-accent-strong text-[11px] font-semibold tracking-widest">
                AND
              </div>
            )}
            <div className="flex items-start gap-3">
              <label htmlFor={inputId} className="w-32 shrink-0 pt-1.5">
                <span className="text-theme-text block text-xs font-medium">{meta.label}</span>
                <span className="text-theme-muted block text-[11px]">{meta.hint}</span>
              </label>
              <div className="min-w-0 flex-1">
                <FilterConditionInput
                  conditionKey={key}
                  value={group[key]}
                  inputId={inputId}
                  onChange={(v) => {
                    update(key, v);
                  }}
                />
              </div>
              <button
                type="button"
                onClick={() => {
                  remove(key);
                }}
                className="text-theme-text-secondary pt-1.5 text-xs hover:opacity-70"
                aria-label={`Remove ${meta.label} from ${title}`}
              >
                ×
              </button>
            </div>
          </Fragment>
        );
      })}

      {addable.length > 0 && (
        <select
          aria-label={`Add a condition to ${title}`}
          value=""
          onChange={(e) => {
            if (e.target.value)
              update(
                e.target.value as FilterConditionKey,
                emptyValue(e.target.value as FilterConditionKey),
              );
          }}
          className="border-theme-line bg-theme-panel-strong text-theme-text-secondary rounded-[var(--radius-sm)] border px-2 py-1 text-xs"
        >
          <option value="">+ Add condition (AND)…</option>
          {addable.map((c) => (
            <option key={c.key} value={c.key}>
              {c.label}
            </option>
          ))}
        </select>
      )}
    </fieldset>
  );
}
