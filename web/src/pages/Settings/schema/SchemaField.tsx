// One setting on the schema-driven Settings section (#158), rendered from its
// schema entry by type: a checkbox, a number or text input, a select for an
// enum, a one-per-line list or `key = value` map, or read-only JSON for a
// value edited elsewhere. No setting has form code of its own.

import { useId, useState } from 'react';
import {
  fieldText,
  parseFieldInput,
  settingsLayerLabel,
  type SettingsFieldDef,
} from '../../../lib/settingsSchema';
import { checkboxCls, helperTextCls, inputCls, selectCls, textareaCls } from '../formStyles';

export interface SchemaFieldProps {
  def: SettingsFieldDef;
  /** The value shown: the draft, this layer's value, or the inherited one. */
  value: unknown;
  /** True when this layer sets the value (or the draft does). */
  setHere: boolean;
  /** Where an inherited value comes from. */
  source: string;
  changed: boolean;
  onChange: (value: unknown) => void;
  onError: (error: string | null) => void;
  /** Removes the value from this layer, so a lower layer decides it. */
  onUnset: () => void;
}

export function SchemaField({
  def,
  value,
  setHere,
  source,
  changed,
  onChange,
  onError,
  onUnset,
}: SchemaFieldProps) {
  const id = useId();
  const [text, setText] = useState(() => fieldText(def, value));
  const [error, setError] = useState<string | null>(null);

  const onText = (next: string) => {
    setText(next);
    const parsed = parseFieldInput(def, next);
    const err = parsed.ok ? null : parsed.error;
    setError(err);
    onError(err);
    if (parsed.ok) onChange(parsed.value);
  };

  let control;
  switch (def.kind) {
    case 'boolean':
      control = (
        <input
          id={id}
          type="checkbox"
          className={checkboxCls}
          checked={value === true}
          onChange={(e) => {
            onChange(e.target.checked);
          }}
        />
      );
      break;
    case 'enum':
      control = (
        <select
          id={id}
          className={selectCls}
          value={typeof value === 'string' ? value : ''}
          onChange={(e) => {
            onChange(e.target.value);
          }}
        >
          {def.enum.map((opt) => (
            <option key={opt} value={opt}>
              {opt === '' ? '(default)' : opt}
            </option>
          ))}
        </select>
      );
      break;
    case 'list':
    case 'map':
      control = (
        <textarea
          id={id}
          className={textareaCls}
          value={text}
          placeholder={def.kind === 'map' ? 'key = value, one per line' : 'one per line'}
          onChange={(e) => {
            onText(e.target.value);
          }}
        />
      );
      break;
    case 'json':
      control = (
        <pre
          id={id}
          className="bg-theme-bg-soft text-theme-text-secondary max-h-40 overflow-auto rounded-[var(--radius-sm)] p-2 font-mono text-[11px]"
        >
          {fieldText(def, value) || '(not set)'}
        </pre>
      );
      break;
    default:
      control = (
        <input
          id={id}
          type={def.kind === 'string' ? 'text' : 'number'}
          className={inputCls}
          value={text}
          min={def.minimum}
          onChange={(e) => {
            onText(e.target.value);
          }}
        />
      );
  }

  return (
    <div className="space-y-1" data-testid={`setting-${def.key}`}>
      <div className="flex flex-wrap items-baseline gap-2">
        <label htmlFor={id} className="text-theme-text text-sm font-medium">
          {def.label}
        </label>
        <code className="text-theme-muted text-[11px]">{def.key}</code>
        <span className="text-theme-muted text-[11px]">
          {changed
            ? 'changed'
            : setHere
              ? 'set in this layer'
              : `from ${settingsLayerLabel(source)}`}
          {' · '}
          {def.live ? 'applies at once' : 'applies after a restart'}
        </span>
        {setHere && def.kind !== 'json' && (
          <button
            type="button"
            className="text-theme-accent-text text-[11px] underline"
            onClick={onUnset}
          >
            Unset here
          </button>
        )}
      </div>
      {control}
      {def.description && <p className={helperTextCls}>{def.description}</p>}
      {def.kind === 'json' && (
        <p className={helperTextCls}>
          {def.editor ? `Edited on the ${def.editor} page.` : 'Edit it in the JSON view.'}
        </p>
      )}
      {error && (
        <p role="alert" className="text-theme-danger-text text-xs">
          {error}
        </p>
      )}
    </div>
  );
}
