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

  return (
    <fieldset className={fieldSurfaceCls} data-testid={`rule-${String(index)}`}>
      <legend className="sr-only">Rule {index + 1}</legend>
      <div className="grid gap-x-2.5 gap-y-3 sm:grid-cols-[7.5rem_minmax(0,1fr)]">
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
            className={cn(textareaCls, 'min-h-10 font-sans text-sm')}
            value={rule.text}
            onChange={(e) => {
              onChange({ ...rule, text: e.target.value });
            }}
          />
        </div>
        <div>
          <label className={fieldLabelCls} htmlFor={`${uid}-id`}>
            ID
          </label>
          <input
            id={`${uid}-id`}
            className={cn(inputCls, 'font-mono text-[13px]')}
            value={rule.id}
            onChange={(e) => {
              onChange({ ...rule, id: e.target.value });
            }}
          />
        </div>
        <div>
          <label className={fieldLabelCls} htmlFor={`${uid}-description`}>
            Description (not sent to the agent)
          </label>
          <input
            id={`${uid}-description`}
            className={inputCls}
            value={rule.description ?? ''}
            onChange={(e) => {
              onChange({
                ...rule,
                description: e.target.value === '' ? undefined : e.target.value,
              });
            }}
          />
        </div>
      </div>
      <div className="grid gap-2.5 sm:grid-cols-3">
        {SCOPE_FIELDS.map((f) => (
          <div key={f.key}>
            <label className={fieldLabelCls} htmlFor={`${uid}-${f.key}`}>
              {f.label}
            </label>
            {/* Uncontrolled: keeps the operator's typing (spaces, a trailing
                comma) while each change commits the parsed list. */}
            <input
              id={`${uid}-${f.key}`}
              className={cn(inputCls, 'min-h-8 rounded-full py-1 text-xs')}
              defaultValue={(rule.scope?.[f.key] ?? []).join(', ')}
              placeholder={f.placeholder}
              onChange={(e) => {
                setScope(f.key, e.target.value);
              }}
            />
          </div>
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <p className="text-theme-muted text-xs">
          Scope lists are comma-separated; * and ? match any text.
        </p>
        <Button
          variant="secondary"
          size="xs"
          pill
          className="text-theme-danger-text ml-auto"
          onClick={onRemove}
          aria-label={`Remove rule ${rule.id || String(index + 1)}`}
        >
          Remove
        </Button>
      </div>
      {error && (
        <p role="alert" className="text-theme-danger-text text-xs">
          {error}
        </p>
      )}
    </fieldset>
  );
}
