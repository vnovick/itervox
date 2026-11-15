// Rules editor (#157): one tab per writable rules layer (user, project,
// project local), a form and a JSON view of each, and a preview of the
// effective rules for a profile and an issue.

import type { KeyboardEvent } from 'react';
import PageMeta from '../../components/common/PageMeta';
import { useRules } from '../../queries/rules';
import { useUIStore } from '../../store/uiStore';
import { RULES_LAYER_NAMES } from '../../types/schemas';
import { RulesLayerEditor } from './RulesLayerEditor';
import { LAYER_INFO } from './rulesModel';
import { RulesPreviewPanel } from './RulesPreviewPanel';

export default function Rules() {
  const { data, isLoading, isError } = useRules();
  const current = useUIStore((s) => s.rulesLayer);
  const setCurrent = useUIStore((s) => s.setRulesLayer);
  const layer = data?.layers.find((l) => l.name === current);

  const onKeyDown = (e: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const n = RULES_LAYER_NAMES.length;
    const to =
      e.key === 'ArrowRight' ? (index + 1) % n : e.key === 'ArrowLeft' ? (index - 1 + n) % n : -1;
    if (to < 0) return;
    e.preventDefault();
    setCurrent(RULES_LAYER_NAMES[to]);
    document.getElementById(`rules-tab-${RULES_LAYER_NAMES[to]}`)?.focus();
  };

  return (
    <>
      <PageMeta
        title="Itervox | Rules"
        description="Itervox rules — instructions every agent run follows"
      />
      <div className="w-full max-w-6xl space-y-6">
        <div>
          <h1 className="text-theme-text text-2xl font-bold tracking-tight">Rules</h1>
          <p className="text-theme-muted mt-1 text-sm">
            Short instructions every run follows: workers, reviewers and the dependency analyzer.
            The rules in scope go into the prompt as a <code>## Rules</code> block ahead of the
            profile’s SOUL.md and INSTRUCTIONS.md, must rules first. A later layer replaces a rule
            with the same id, except a user must rule. Edits apply to the next run; no restart.
          </p>
        </div>
        <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,24rem)]">
          <section className="space-y-4">
            <div
              role="tablist"
              aria-label="Rules layers"
              className="border-theme-line flex gap-1 border-b"
            >
              {RULES_LAYER_NAMES.map((name, i) => {
                const active = name === current;
                return (
                  <button
                    key={name}
                    id={`rules-tab-${name}`}
                    type="button"
                    role="tab"
                    aria-selected={active}
                    aria-controls="rules-layer-panel"
                    tabIndex={active ? 0 : -1}
                    className={`-mb-px border-b-2 px-3 py-2 text-sm font-medium ${
                      active
                        ? 'border-theme-accent text-theme-text'
                        : 'text-theme-muted hover:text-theme-text border-transparent'
                    }`}
                    onClick={() => {
                      setCurrent(name);
                    }}
                    onKeyDown={(e) => {
                      onKeyDown(e, i);
                    }}
                  >
                    {LAYER_INFO[name].label}
                  </button>
                );
              })}
            </div>
            <div id="rules-layer-panel" role="tabpanel" aria-labelledby={`rules-tab-${current}`}>
              {isLoading && <p className="text-theme-muted text-sm">Loading rules…</p>}
              {isError && (
                <p className="text-theme-danger-text text-sm">Could not load the rules.</p>
              )}
              {data && layer && <RulesLayerEditor layer={layer} schemaUrl={data.schemaUrl} />}
            </div>
          </section>
          <RulesPreviewPanel />
        </div>
      </div>
    </>
  );
}
