// Schema-driven settings (#158): every layered setting (agent, polling,
// server, workspace), rendered from the settings JSON Schema the daemon
// serves. A tab picks the layer a save writes — user, project or project
// local — and each layer has a form view and a JSON view of its file. Saves go
// through the daemon's comment-preserving JSONC writer.

import { useMemo, useState, type KeyboardEvent, type ReactNode } from 'react';
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
import { cn } from '../../../components/ui/cn';
import { inputCls } from '../formStyles';
import { SchemaSettingsForm } from './SchemaSettingsForm';
import { settingsSectionId, settingsSectionTitle } from './settingsSections';
import { SettingsLayerJson } from './SettingsLayerJson';

const VIEWS = [
  { id: 'form', label: 'Form' },
  { id: 'json', label: 'JSON' },
] as const;

interface SchemaSettingsCardProps {
  /** The section heading, drawn at the start of the toolbar row. */
  heading?: ReactNode;
  /** Help text under the toolbar. */
  help?: ReactNode;
}

export function SchemaSettingsCard({ heading, help }: SchemaSettingsCardProps = {}) {
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
  const sections = useMemo(() => [...new Set(fields.map((f) => f.section))], [fields]);
  const [filter, setFilter] = useState('');
  const [openSections, setOpenSections] = useState<ReadonlySet<string>>(() => new Set());
  const toggleSection = (section: string, open: boolean) => {
    setOpenSections((prev) => {
      const next = new Set(prev);
      if (open) next.add(section);
      else next.delete(section);
      return next;
    });
  };
  const formView = view !== 'json';

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
    return (
      <div className="space-y-2">
        {heading}
        <p className="text-theme-danger-text text-sm">Could not load the settings.</p>
      </div>
    );
  }
  if (!schema.data || !layers.data) {
    return (
      <div className="space-y-2">
        {heading}
        <p className="text-theme-muted text-sm">Loading settings…</p>
      </div>
    );
  }
  const name = current as SettingsLayerName;
  const draftKey = `${current}-${layer?.version ?? ''}`;

  return (
    <div className="space-y-4">
      {/* Toolbar: heading, layer tabs, filter, section links. */}
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        {heading}
        <div role="tablist" aria-label="Settings layers" className="segmented">
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
                className="text-sm"
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
        {formView && layer?.writable && !layer.error && (
          <>
            <input
              type="search"
              aria-label="Filter settings"
              placeholder="Filter settings"
              className={cn(inputCls, 'w-auto min-w-0 flex-[1_1_220px] rounded-full')}
              value={filter}
              onChange={(e) => {
                setFilter(e.target.value);
              }}
            />
            <nav aria-label="Settings sections" className="flex flex-wrap items-center gap-1">
              {sections.map((section) => (
                <a
                  key={section}
                  href={`#${settingsSectionId(section)}`}
                  className="text-theme-text-secondary hover:text-theme-text rounded-full px-2 py-1 text-[13px]"
                  onClick={(e) => {
                    e.preventDefault();
                    toggleSection(section, true);
                    requestAnimationFrame(() => {
                      // Not every environment implements scrollIntoView (jsdom).
                      const target: Partial<Pick<Element, 'scrollIntoView'>> | null =
                        document.getElementById(settingsSectionId(section));
                      target?.scrollIntoView?.({ block: 'start' });
                    });
                  }}
                >
                  {settingsSectionTitle(section)}
                </a>
              ))}
            </nav>
          </>
        )}
      </div>
      {help}
      <div
        id="settings-layer-panel"
        role="tabpanel"
        aria-labelledby={`settings-tab-${current}`}
        className="space-y-4"
        data-testid={`settings-layer-${current}`}
      >
        {layer && (
          <div className="space-y-1">
            <p
              className="text-theme-text truncate font-mono text-xs"
              title={layer.path || undefined}
            >
              {layer.path || '(off)'}
            </p>
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
            <div role="group" aria-label="Settings view" className="segmented">
              {VIEWS.map((v) => (
                <button
                  key={v.id}
                  type="button"
                  aria-pressed={view === v.id}
                  className="text-sm"
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
                filter={filter}
                openSections={openSections}
                onToggleSection={toggleSection}
              />
            )}
          </>
        )}
      </div>
    </div>
  );
}
