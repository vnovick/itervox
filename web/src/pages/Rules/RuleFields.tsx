// One rule in the Rules editor's form view (#157).

import { useId } from 'react';
import { Button } from '../../components/ui/button';
import { cn } from '../../components/ui/cn';
import type { Rule } from '../../types/schemas';
import { splitList } from './rulesModel';
import {
  fieldLabelCls,
  fieldSurfaceCls,
  inputCls,
  selectCls,
  textareaCls,
} from '../Settings/formStyles';

type ScopeKey = 'projects' | 'profiles' | 'backends';

const SCOPE_FIELDS: { key: ScopeKey; label: string; placeholder: string }[] = [
  { key: 'projects', label: 'Projects', placeholder: 'all projects' },
  {
    key: 'profiles',
    label: 'Profiles',
    placeholder: 'all profiles (a run without one is "default")',
  },
  { key: 'backends', label: 'Backends', placeholder: 'all backends (claude, codex)' },
];

interface RuleFieldsProps {
  rule: Rule;
  index: number;
  error?: string;
  onChange: (rule: Rule) => void;
  onRemove: () => void;
}

export function RuleFields({ rule, index, error, onChange, onRemove }: RuleFieldsProps) {
  const uid = useId();
  const setScope = (key: ScopeKey, value: string) => {
    const scope = { ...rule.scope, [key]: splitList(value) };
    const empty = !scope.projects && !scope.profiles && !scope.backends;
    onChange({ ...rule, scope: empty ? undefined : scope });
  };

  // Compact, three rows: severity + text; id + description; the scope
  // lists as inline chips-sized inputs with Remove at the end. Every field
  // stays editable; inline labels replace the stacked ones.
  const inlineLabel = 'text-theme-text-secondary shrink-0 text-xs';
  const chipInput = cn(
    inputCls,
    'field-sizing-content min-h-7 w-auto max-w-full min-w-[6rem] rounded-full px-2.5 py-0.5 text-xs',
  );
  return (
    <fieldset className={fieldSurfaceCls} data-testid={`rule-${String(index)}`}>
      <legend className="sr-only">Rule {index + 1}</legend>
      <div className="grid items-start gap-x-2.5 gap-y-2 sm:grid-cols-[7.5rem_minmax(0,1fr)]">
        <div>
          <label className={fieldLabelCls} htmlFor={`${uid}-severity`}>
            Severity
          </label>
          <select
            id={`${uid}-severity`}
            className={selectCls}
            value={rule.severity}
            onChange={(e) => {
              onChange({ ...rule, severity: e.target.value === 'must' ? 'must' : 'should' });
            }}
          >
            <option value="must">must</option>
            <option value="should">should</option>
          </select>
        </div>
        <div>
          <label className={fieldLabelCls} htmlFor={`${uid}-text`}>
            Text
          </label>
          <textarea
            id={`${uid}-text`}
            rows={1}
            className={cn(textareaCls, 'field-sizing-content min-h-10 font-sans text-sm')}
            value={rule.text}
            onChange={(e) => {
              onChange({ ...rule, text: e.target.value });
            }}
          />
        </div>
        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1.5 sm:col-start-2">
          <label className={inlineLabel} htmlFor={`${uid}-id`}>
            ID
          </label>
          <input
            id={`${uid}-id`}
            className={cn(chipInput, 'font-mono')}
            value={rule.id}
            onChange={(e) => {
              onChange({ ...rule, id: e.target.value });
            }}
          />
          <label className={inlineLabel} htmlFor={`${uid}-description`}>
            Description<span className="sr-only"> (not sent to the agent)</span>
          </label>
          <input
            id={`${uid}-description`}
            className={cn(chipInput, 'min-w-[10rem] flex-1')}
            placeholder="not sent to the agent"
            value={rule.description ?? ''}
            onChange={(e) => {
              onChange({
                ...rule,
                description: e.target.value === '' ? undefined : e.target.value,
              });
            }}
          />
        </div>
        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1.5 sm:col-start-2">
          {SCOPE_FIELDS.map((f) => (
            <span key={f.key} className="flex max-w-full min-w-0 items-center gap-1.5">
              <label className={inlineLabel} htmlFor={`${uid}-${f.key}`}>
                {f.label}
              </label>
              {/* Uncontrolled: keeps the operator's typing (spaces, a trailing
                  comma) while each change commits the parsed list. Sized to
                  its text, so a placeholder is never clipped. */}
              <input
                id={`${uid}-${f.key}`}
                className={chipInput}
                defaultValue={(rule.scope?.[f.key] ?? []).join(', ')}
                placeholder={f.placeholder}
                title="Comma-separated; * and ? match any text."
                onChange={(e) => {
                  setScope(f.key, e.target.value);
                }}
              />
            </span>
          ))}
          <Button
            variant="glass-danger"
            size="xs"
            pill
            className="ml-auto"
            onClick={onRemove}
            aria-label={`Remove rule ${rule.id || String(index + 1)}`}
          >
            Remove
          </Button>
        </div>
      </div>
      {error && (
        <p role="alert" className="text-theme-danger-text text-xs">
          {error}
        </p>
      )}
    </fieldset>
  );
}
