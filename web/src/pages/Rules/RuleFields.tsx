// One rule in the Rules editor's form view (#157).

import { useId } from 'react';
import { Button } from '../../components/ui/button';
import type { Rule } from '../../types/schemas';
import { splitList } from './rulesModel';
import {
  fieldLabelCls,
  fieldSurfaceCls,
  helperTextCls,
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
      <div className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_9rem_auto] sm:items-end">
        <div>
          <label className={fieldLabelCls} htmlFor={`${uid}-id`}>
            ID
          </label>
          <input
            id={`${uid}-id`}
            className={inputCls}
            value={rule.id}
            onChange={(e) => {
              onChange({ ...rule, id: e.target.value });
            }}
          />
        </div>
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
        <Button
          variant="ghost"
          size="sm"
          onClick={onRemove}
          aria-label={`Remove rule ${rule.id || String(index + 1)}`}
        >
          Remove
        </Button>
      </div>
      <div>
        <label className={fieldLabelCls} htmlFor={`${uid}-text`}>
          Text
        </label>
        <textarea
          id={`${uid}-text`}
          className={textareaCls}
          value={rule.text}
          onChange={(e) => {
            onChange({ ...rule, text: e.target.value });
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
            onChange({ ...rule, description: e.target.value === '' ? undefined : e.target.value });
          }}
        />
      </div>
      <div className="grid gap-3 sm:grid-cols-3">
        {SCOPE_FIELDS.map((f) => (
          <div key={f.key}>
            <label className={fieldLabelCls} htmlFor={`${uid}-${f.key}`}>
              {f.label}
            </label>
            {/* Uncontrolled: keeps the operator's typing (spaces, a trailing
                comma) while each change commits the parsed list. */}
            <input
              id={`${uid}-${f.key}`}
              className={inputCls}
              defaultValue={(rule.scope?.[f.key] ?? []).join(', ')}
              placeholder={f.placeholder}
              onChange={(e) => {
                setScope(f.key, e.target.value);
              }}
            />
          </div>
        ))}
      </div>
      <p className={helperTextCls}>Scope lists are comma-separated; * and ? match any text.</p>
      {error && (
        <p role="alert" className="text-theme-danger-text text-xs">
          {error}
        </p>
      )}
    </fieldset>
  );
}
