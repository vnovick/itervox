// Form view of one settings layer (#158): every field of the settings schema,
// grouped by section and heading, each shown with the value this layer sets or
// the one it inherits. Edits collect in a draft; Save writes only the changed
// keys to the layer the tab names (null unsets a key there).
//
// Sections start collapsed so the page stays short; the filter (in the
// card's toolbar) opens every section with a match, and the toolbar's
// section links open one section on demand.

import { useState } from 'react';
import { Button } from '../../../components/ui/button';
import {
  groupSettingsFields,
  settingsLayerLabel,
  type SettingsFieldDef,
} from '../../../lib/settingsSchema';
import type { SettingsEffective, SettingsLayer } from '../../../types/schemas';
import { SchemaField } from './SchemaField';
import { settingsSectionId, settingsSectionTitle } from './settingsSections';

interface SchemaSettingsFormProps {
  fields: SettingsFieldDef[];
  layer: SettingsLayer;
  effective: Map<string, SettingsEffective>;
  saving: boolean;
  onSave: (values: Record<string, unknown>) => void;
  /** Text from the toolbar's filter; a match opens its section. */
  filter?: string;
  /** Sections the operator has expanded. */
  openSections?: ReadonlySet<string>;
  onToggleSection?: (section: string, open: boolean) => void;
}

function without<T>(record: Record<string, T>, key: string): Record<string, T> {
  return Object.fromEntries(Object.entries(record).filter(([k]) => k !== key));
}

const NO_SECTIONS: ReadonlySet<string> = new Set();

export function SchemaSettingsForm({
  fields,
  layer,
  effective,
  saving,
  onSave,
  filter = '',
  openSections = NO_SECTIONS,
  onToggleSection,
}: SchemaSettingsFormProps) {
  const [draft, setDraft] = useState<Record<string, unknown>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [resets, setResets] = useState<Record<string, number>>({});

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
    <div className="space-y-2">
      {sections.map((section) => (
        <details
          key={section}
          id={settingsSectionId(section)}
          open={needle !== '' || openSections.has(section)}
          onToggle={(e) => {
            if (needle !== '') return;
            const open = e.currentTarget.open;
            if (open !== openSections.has(section)) onToggleSection?.(section, open);
          }}
          className="glass-card scroll-mt-24"
        >
          <summary className="text-theme-text flex cursor-pointer items-center gap-2 px-4 py-3 text-sm font-semibold">
            {settingsSectionTitle(section)}
            <span className="text-theme-muted text-xs font-normal">
              {(() => {
                const n = visible.filter((f) => f.section === section).length;
                return `${String(n)} setting${n === 1 ? '' : 's'}`;
              })()}
            </span>
          </summary>
          <div className="space-y-5 px-4 pb-4">
            {groupSettingsFields(visible.filter((f) => f.section === section)).map(
              ([group, list]) => (
                <fieldset key={`${section}-${group}`} className="space-y-3">
                  <legend className="eyebrow text-theme-text-secondary mb-1">{group}</legend>
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
      <div className="flex items-center gap-3 pt-2">
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
