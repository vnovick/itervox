// Rules editor preview (#157): the effective rules a run of a profile on an
// issue would get, merged across the layers and filtered by scope, with the
// conflicts and broken layers that change them.

import { useId, useState } from 'react';
import Badge from '../../components/ui/badge/Badge';
import { useRulesPreview } from '../../queries/rules';
import { useItervoxStore } from '../../store/itervoxStore';
import { useUIStore } from '../../store/uiStore';
import { fieldLabelCls, inputCls, selectCls } from '../Settings/formStyles';
import { layerLabel } from './rulesModel';

const EMPTY_PROFILES: string[] = [];

export function RulesPreviewPanel() {
  const uid = useId();
  const profiles = useItervoxStore((s) => s.snapshot?.availableProfiles ?? EMPTY_PROFILES);
  const profile = useUIStore((s) => s.rulesPreviewProfile);
  const setProfile = useUIStore((s) => s.setRulesPreviewProfile);
  const issue = useUIStore((s) => s.rulesPreviewIssue);
  const setIssue = useUIStore((s) => s.setRulesPreviewIssue);
  const [issueDraft, setIssueDraft] = useState(issue);
  const { data: preview, isError } = useRulesPreview(profile, issue);

  return (
    <section
      aria-labelledby={`${uid}-title`}
      className="border-theme-line bg-theme-bg-elevated space-y-4 rounded-[var(--radius-md)] border p-4"
    >
      <h2 id={`${uid}-title`} className="text-theme-text text-sm font-semibold">
        Effective rules
      </h2>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-1 xl:grid-cols-2">
        <div>
          <label className={fieldLabelCls} htmlFor={`${uid}-profile`}>
            Profile
          </label>
          <select
            id={`${uid}-profile`}
            className={selectCls}
            value={profile}
            onChange={(e) => {
              setProfile(e.target.value);
            }}
          >
            <option value="">The issue’s profile, else default</option>
            {profiles.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label className={fieldLabelCls} htmlFor={`${uid}-issue`}>
            Issue
          </label>
          <input
            id={`${uid}-issue`}
            className={inputCls}
            placeholder="e.g. ENG-12"
            value={issueDraft}
            onChange={(e) => {
              setIssueDraft(e.target.value);
            }}
            onBlur={() => {
              setIssue(issueDraft.trim());
            }}
            onKeyDown={(e) => {
              if (e.key === 'Enter') setIssue(issueDraft.trim());
            }}
          />
        </div>
      </div>
      {isError && <p className="text-theme-danger-text text-xs">Could not load the preview.</p>}
      {preview && (
        <div className="space-y-3" data-testid="rules-preview">
          <p className="text-theme-muted text-xs">
            Profile <code>{preview.profile}</code> on <code>{preview.backend}</code>
            {preview.projects.length > 0 && <> in {preview.projects.join(' / ')}</>}
            {preview.hash && (
              <>
                {' '}
                · rule set <code>{preview.hash}</code>
              </>
            )}
          </p>
          {preview.templatePlacesRules && (
            <p className="text-theme-muted text-xs">
              The prompt places the rules itself with <code>{'{{ rules }}'}</code>, so no automatic
              block is added.
            </p>
          )}
          {[
            ...preview.conflicts.map((c) => ({ key: `c-${c.layer}-${c.id}`, text: c.message })),
            ...preview.errors.map((e) => ({
              key: `e-${e.layer}`,
              text: `${layerLabel(e.layer)} layer ignored: ${e.message}`,
            })),
          ].map((p) => (
            <p key={p.key} role="alert" className="text-theme-warning-text text-xs">
              {p.text}
            </p>
          ))}
          {preview.rules.length === 0 ? (
            <p className="text-theme-muted text-sm">No rules apply to this run.</p>
          ) : (
            <ul className="space-y-2">
              {preview.rules.map((r) => (
                <li key={`${r.layer}-${r.id}`} className="text-sm">
                  <Badge size="sm" color={r.severity === 'must' ? 'error' : 'info'}>
                    {r.severity}
                  </Badge>{' '}
                  <span className="text-theme-text">{r.text}</span>{' '}
                  <span className="text-theme-muted text-xs">
                    ({r.id}, {layerLabel(r.layer)}
                    {r.overrides && <>, replaces {layerLabel(r.overrides)}</>})
                  </span>
                </li>
              ))}
            </ul>
          )}
          {preview.block && (
            <details>
              <summary className="text-theme-text-secondary cursor-pointer text-xs">
                Prompt block
              </summary>
              <pre className="bg-theme-bg-soft mt-2 overflow-x-auto rounded p-3 text-[11px] whitespace-pre-wrap">
                {preview.block}
              </pre>
            </details>
          )}
        </div>
      )}
    </section>
  );
}
