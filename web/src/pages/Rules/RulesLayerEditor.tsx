// One rules layer in the Rules editor (#157): where the file is, whether it is
// committed, and the form or JSON view of its rules.

import { Banner } from '../../components/ui/Banner';
import { useSaveRulesLayer } from '../../queries/rules';
import { useUIStore } from '../../store/uiStore';
import type { RulesLayer, RulesLayerName } from '../../types/schemas';
import { RulesForm } from './RulesForm';
import { RulesJsonEditor } from './RulesJsonEditor';
import { LAYER_INFO, newRulesFile } from './rulesModel';

const VIEWS = [
  { id: 'form', label: 'Form' },
  { id: 'json', label: 'JSON' },
] as const;

export function RulesLayerEditor({ layer, schemaUrl }: { layer: RulesLayer; schemaUrl: string }) {
  const view = useUIStore((s) => s.rulesView);
  const setView = useUIStore((s) => s.setRulesView);
  const save = useSaveRulesLayer();
  const name = layer.name as RulesLayerName;
  const info = LAYER_INFO[name];
  // Drafts start over from the file whenever its version changes (a save, or
  // a reload after someone else changed it).
  const draftKey = `${layer.name}-${layer.version}`;

  return (
    <div className="space-y-4" data-testid={`rules-layer-${layer.name}`}>
      <div className="space-y-1">
        <p className="text-theme-text font-mono text-xs break-all">{layer.path || '(off)'}</p>
        <p className="text-theme-muted text-xs">
          {info.git}{' '}
          {!layer.exists && layer.writable && 'The file does not exist yet; saving creates it.'}
        </p>
      </div>
      {layer.error && (
        <Banner tone="danger" className="rounded-[var(--radius-sm)]">
          This file is not used until it is fixed: {layer.error}
        </Banner>
      )}
      {!layer.writable ? (
        <p className="text-theme-muted text-sm">This layer is turned off.</p>
      ) : (
        <>
          <div role="group" aria-label="Editor view" className="flex gap-1">
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
          {view === 'form' && layer.error ? (
            <p className="text-theme-muted text-sm">
              Fix the file in the JSON view to edit it as a form.
            </p>
          ) : view === 'form' ? (
            <RulesForm
              key={draftKey}
              rules={layer.rules}
              saving={save.isPending}
              onSave={(rules) => {
                save.mutate({ layer: name, baseVersion: layer.version, rules });
              }}
            />
          ) : (
            <RulesJsonEditor
              key={draftKey}
              initialText={layer.exists ? layer.raw : newRulesFile(schemaUrl)}
              schemaUrl={schemaUrl}
              saving={save.isPending}
              onSave={(raw) => {
                save.mutate({ layer: name, baseVersion: layer.version, raw });
              }}
            />
          )}
        </>
      )}
    </div>
  );
}
