import { TagInput } from '../../../components/itervox/TagInput';
import { fieldLabelCls, helperTextCls } from '../formStyles';
import type { AutomationFormValues } from './automationForm';
import {
  PR_CHECK_CONCLUSIONS,
  PR_EVENT_FILTER_FIELDS,
  PR_EVENT_FILTER_TRIGGER,
  PR_MERGEABLE_STATES,
  PR_REVIEW_STATES,
  type PREventFilterField,
} from './prEventFilters';

const FIELD_META: Record<
  PREventFilterField,
  { label: string; placeholder: string; suggestions: string[]; help: string }
> = {
  checkNames: {
    label: 'Check names',
    placeholder: '+ Add check',
    suggestions: [],
    help: 'Check run names or commit status contexts, e.g. CI or Chromatic. Empty = every check.',
  },
  conclusions: {
    label: 'Conclusions',
    placeholder: '+ Add conclusion',
    suggestions: PR_CHECK_CONCLUSIONS,
    help: 'Empty = every conclusion. Use failure (and timed_out) for a fix loop.',
  },
  reviewAuthors: {
    label: 'Review authors',
    placeholder: '+ Add GitHub login',
    suggestions: [],
    help: 'GitHub logins, e.g. greptile-apps[bot]. Empty = every reviewer.',
  },
  reviewStates: {
    label: 'Review states',
    placeholder: '+ Add state',
    suggestions: PR_REVIEW_STATES,
    help: 'Empty = every submitted review.',
  },
  mergeableStates: {
    label: 'Mergeable states',
    placeholder: '+ Add state',
    suggestions: PR_MERGEABLE_STATES,
    help: 'Fire on a move into these states. Empty = a conflict appearing (dirty) or clearing.',
  },
};

// PREventFilterFields renders the #173 PR-event filters for the selected
// PR-event trigger. Pure presentational; the parent's form owns the values.
export function PREventFilterFields({
  values,
  onChange,
}: {
  values: AutomationFormValues;
  onChange: (field: PREventFilterField, value: string[]) => void;
}) {
  const fields = PR_EVENT_FILTER_FIELDS.filter(
    (field) => PR_EVENT_FILTER_TRIGGER[field] === values.triggerType,
  );
  return (
    <div className="border-theme-line bg-theme-bg-soft space-y-3 rounded-[var(--radius-sm)] border px-3 py-3">
      <p className={helperTextCls}>
        Fires only for pull requests an Itervox run opened or continued, from GitHub webhooks
        (pr_events.webhook_secret) or a poll of those PRs.
      </p>
      {fields.map((field) => {
        const meta = FIELD_META[field];
        return (
          <div key={field}>
            <label htmlFor={`automation-pr-${field}`} className={fieldLabelCls}>
              {meta.label}
            </label>
            <TagInput
              id={`automation-pr-${field}`}
              chips={values[field]}
              onChange={(next) => {
                onChange(field, next);
              }}
              placeholder={meta.placeholder}
              suggestions={meta.suggestions}
              chipClassName="bg-[var(--bg-soft)] text-[var(--text-secondary)]"
              addButtonClassName="bg-[var(--bg-soft)] text-[var(--text-secondary)] hover:opacity-80"
            />
            <p className={helperTextCls}>{meta.help}</p>
          </div>
        );
      })}
    </div>
  );
}
