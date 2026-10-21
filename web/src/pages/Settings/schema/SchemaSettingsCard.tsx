// Schema-driven settings (#158): every layered setting (agent, polling,
// server, workspace), rendered from the settings JSON Schema the daemon
// serves. A tab picks the layer a save writes — user, project or project
// local — and each layer has a form view and a JSON view of its file. Saves go
// through the daemon's comment-preserving JSONC writer.

import { useMemo, type KeyboardEvent } from 'react';
import { Banner } from '../../../components/ui/Banner';
import {
  flattenSettingsSchema,
  newSettingsFile,
  SETTINGS_LAYER_INFO,
  settingsLayerLabel,
} from '../../../lib/settingsSchema';
import {
  useSaveSettingsLayer,
  useSettingsLayers,
  useSettingsSchema,
} from '../../../queries/settingsLayers';
import { useUIStore } from '../../../store/uiStore';
import { SETTINGS_LAYER_NAMES, type SettingsLayerName } from '../../../types/schemas';
import { SchemaSettingsForm } from './SchemaSettingsForm';
import { SettingsLayerJson } from './SettingsLayerJson';

const VIEWS = [
  { id: 'form', label: 'Form' },
  { id: 'json', label: 'JSON' },
] as const;

export function SchemaSettingsCard() {
  const schema = useSettingsSchema();
  const layers = useSettingsLayers();
  const save = useSaveSettingsLayer();
  const current = useUIStore((s) => s.settingsLayer);
  const setCurrent = useUIStore((s) => s.setSettingsLayer);
  const view = useUIStore((s) => s.settingsView);
  const setView = useUIStore((s) => s.setSettingsView);

  const fields = useMemo(
    () => (schema.data ? flattenSettingsSchema(schema.data) : []),
    [schema.data],
  );
  const effective = useMemo(
    () => new Map((layers.data?.effective ?? []).map((e) => [e.key, e])),
    [layers.data],
  );
  const layer = layers.data?.layers.find((l) => l.name === current);

  const onKeyDown = (e: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const n = SETTINGS_LAYER_NAMES.length;
    const to =
      e.key === 'ArrowRight' ? (index + 1) % n : e.key === 'ArrowLeft' ? (index - 1 + n) % n : -1;
    if (to < 0) return;
    e.preventDefault();
    setCurrent(SETTINGS_LAYER_NAMES[to]);
    document.getElementById(`settings-tab-${SETTINGS_LAYER_NAMES[to]}`)?.focus();
  };

  if (schema.isError || layers.isError) {
    return <p className="text-theme-danger-text text-sm">Could not load the settings.</p>;
  }
  if (!schema.data || !layers.data) {
    return <p className="text-theme-muted text-sm">Loading settings…</p>;
  }
  const name = current as SettingsLayerName;
  const draftKey = `${current}-${layer?.version ?? ''}`;

  return (
    <div className="border-theme-line bg-theme-bg-elevated space-y-4 rounded-[var(--radius-md)] border p-5">
      <div
        role="tablist"
        aria-label="Settings layers"
        className="border-theme-line flex gap-1 border-b"
      >
        {SETTINGS_LAYER_NAMES.map((n, i) => {
          const active = n === current;
          return (
            <button
              key={n}
              id={`settings-tab-${n}`}
              type="button"
              role="tab"
              aria-selected={active}
              aria-controls="settings-layer-panel"
              tabIndex={active ? 0 : -1}
              className={`-mb-px border-b-2 px-3 py-2 text-sm font-medium ${
                active
                  ? 'border-theme-accent text-theme-text'
                  : 'text-theme-muted hover:text-theme-text border-transparent'
              }`}
              onClick={() => {
                setCurrent(n);
              }}
              onKeyDown={(e) => {
                onKeyDown(e, i);
              }}
            >
              {settingsLayerLabel(n)}
            </button>
          );
        })}
      </div>
      <div
        id="settings-layer-panel"
        role="tabpanel"
        aria-labelledby={`settings-tab-${current}`}
        className="space-y-4"
        data-testid={`settings-layer-${current}`}
      >
        {layer && (
          <div className="space-y-1">
            <p className="text-theme-text font-mono text-xs break-all">{layer.path || '(off)'}</p>
            <p className="text-theme-muted text-xs">
              {SETTINGS_LAYER_INFO[name]?.git}{' '}
              {!layer.exists && layer.writable && 'The file does not exist yet; saving creates it.'}
            </p>
          </div>
        )}
        {layer?.error && (
          <Banner tone="danger" className="rounded-[var(--radius-sm)]">
            This file does not load until it is fixed: {layer.error}
          </Banner>
        )}
        {layer && layer.unknown.length > 0 && (
          <Banner tone="warning" className="rounded-[var(--radius-sm)]">
            Itervox does not read: {layer.unknown.join(', ')}
          </Banner>
        )}
        {!layer?.writable ? (
          <p className="text-theme-muted text-sm">This layer is turned off.</p>
        ) : (
          <>
            <div role="group" aria-label="Settings view" className="flex gap-1">
              {VIEWS.map((v) => (
                <button
                  key={v.id}
                  type="button"
                  aria-pressed={view === v.id}
                  className={`rounded-[var(--radius-sm)] px-3 py-1 text-xs font-medium ${
                    view === v.id
                      ? 'bg-theme-accent text-white'
                      : 'text-theme-text-secondary hover:bg-theme-panel-strong'
                  }`}
                  onClick={() => {
                    setView(v.id);
                  }}
                >
                  {v.label}
                </button>
              ))}
            </div>
            {view === 'json' ? (
              <SettingsLayerJson
                key={draftKey}
                layerName={layer.name}
                initialText={layer.exists ? layer.raw : newSettingsFile(layers.data.schemaUrl)}
                schemaUrl={layers.data.schemaUrl}
                fields={fields}
                saving={save.isPending}
                onSave={(raw) => {
                  save.mutate({ layer: name, baseVersion: layer.version, raw });
                }}
              />
            ) : layer.error ? (
              <p className="text-theme-muted text-sm">
                Fix the file in the JSON view to edit it as a form.
              </p>
            ) : (
              <SchemaSettingsForm
                key={draftKey}
                fields={fields}
                layer={layer}
                effective={effective}
                saving={save.isPending}
                onSave={(values) => {
                  save.mutate({ layer: name, baseVersion: layer.version, values });
                }}
              />
            )}
          </>
        )}
      </div>
    </div>
  );
}
