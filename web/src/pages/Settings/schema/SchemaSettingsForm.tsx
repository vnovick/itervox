// Form view of one settings layer (#158): every field of the settings schema,
// grouped by section and heading, each shown with the value this layer sets or
// the one it inherits. Edits collect in a draft; Save writes only the changed
// keys to the layer the tab names (null unsets a key there).

import { useState } from 'react';
import { Button } from '../../../components/ui/button';
import {
  groupSettingsFields,
  settingsLayerLabel,
  type SettingsFieldDef,
} from '../../../lib/settingsSchema';
import type { SettingsEffective, SettingsLayer } from '../../../types/schemas';
import { inputCls } from '../formStyles';
import { SchemaField } from './SchemaField';

interface SchemaSettingsFormProps {
  fields: SettingsFieldDef[];
  layer: SettingsLayer;
  effective: Map<string, SettingsEffective>;
  saving: boolean;
  onSave: (values: Record<string, unknown>) => void;
}

function without<T>(record: Record<string, T>, key: string): Record<string, T> {
  return Object.fromEntries(Object.entries(record).filter(([k]) => k !== key));
}

const SECTION_TITLES: Record<string, string> = {
  agent: 'Agent',
  polling: 'Polling',
  server: 'Server',
  workspace: 'Workspace',
};

export function SchemaSettingsForm({
  fields,
  layer,
  effective,
  saving,
  onSave,
}: SchemaSettingsFormProps) {
  const [draft, setDraft] = useState<Record<string, unknown>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [resets, setResets] = useState<Record<string, number>>({});
  const [filter, setFilter] = useState('');

  const needle = filter.trim().toLowerCase();
  const visible = needle
    ? fields.filter((f) => `${f.key} ${f.label}`.toLowerCase().includes(needle))
    : fields;
  const sections = [...new Set(visible.map((f) => f.section))];
  const changes = Object.keys(draft).length;
  const blocked = Object.keys(errors).length > 0;

  const setValue = (key: string, value: unknown) => {
    setDraft((d) => ({ ...d, [key]: value }));
  };
  const setError = (key: string, err: string | null) => {
    setErrors((e) => (err ? { ...e, [key]: err } : without(e, key)));
  };

  return (
    <div className="space-y-4">
      <input
        type="search"
        aria-label="Filter settings"
        placeholder="Filter settings"
        className={inputCls}
        value={filter}
        onChange={(e) => {
          setFilter(e.target.value);
        }}
      />
      {sections.map((section) => (
        <details key={section} open className="border-theme-line rounded-[var(--radius-sm)] border">
          <summary className="text-theme-text cursor-pointer px-3 py-2 text-sm font-semibold">
            {SECTION_TITLES[section] ?? section}
          </summary>
          <div className="space-y-5 px-3 pb-3">
            {groupSettingsFields(visible.filter((f) => f.section === section)).map(
              ([group, list]) => (
                <fieldset key={`${section}-${group}`} className="space-y-3">
                  <legend className="text-theme-text-secondary mb-1 text-xs font-semibold tracking-wider uppercase">
                    {group}
                  </legend>
                  {list.map((def) => {
                    const inDraft = def.key in draft;
                    const inLayer = def.key in layer.values;
                    const draftValue = draft[def.key];
                    const removed = inDraft && draftValue === null;
                    const eff = effective.get(def.key);
                    const value =
                      inDraft && !removed
                        ? draftValue
                        : inLayer && !removed
                          ? layer.values[def.key]
                          : (eff?.value ?? def.default);
                    return (
                      <SchemaField
                        key={`${def.key}-${String(resets[def.key] ?? 0)}`}
                        def={def}
                        value={value}
                        setHere={(inDraft && !removed) || (inLayer && !removed)}
                        source={eff?.layer ?? 'default'}
                        changed={inDraft}
                        onChange={(v) => {
                          setValue(def.key, v);
                        }}
                        onError={(err) => {
                          setError(def.key, err);
                        }}
                        onUnset={() => {
                          setError(def.key, null);
                          if (inLayer) setValue(def.key, null);
                          else setDraft((d) => without(d, def.key));
                          setResets((r) => ({ ...r, [def.key]: (r[def.key] ?? 0) + 1 }));
                        }}
                      />
                    );
                  })}
                </fieldset>
              ),
            )}
          </div>
        </details>
      ))}
      {visible.length === 0 && <p className="text-theme-muted text-sm">No setting matches.</p>}
      <div className="flex items-center gap-3">
        <Button
          variant="primary"
          size="sm"
          disabled={saving || changes === 0 || blocked}
          onClick={() => {
            onSave(draft);
          }}
        >
          {saving
            ? 'Saving…'
            : `Save to ${settingsLayerLabel(layer.name)}${changes ? ` (${String(changes)})` : ''}`}
        </Button>
        {changes > 0 && (
          <Button
            size="sm"
            onClick={() => {
              setDraft({});
              setErrors({});
              setResets((r) => {
                const next = { ...r };
                for (const k of Object.keys(draft)) next[k] = (next[k] ?? 0) + 1;
                return next;
              });
            }}
          >
            Discard changes
          </Button>
        )}
      </div>
    </div>
  );
}
