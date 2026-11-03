import { lazy, Suspense, useDeferredValue, useMemo, useState, type ReactNode } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { proseClass } from '../../../utils/format';
import { fieldLabelCls, helperTextCls, textareaCls } from '../formStyles';
import { PROFILE_VARIABLES, type Completion } from './promptCompletions';
import type { PromptProfileContext } from './promptEditor/PromptCodeEditor';

// CodeMirror lives in its own chunk, fetched the first time a prompt editor
// opens (#122), so it stays out of the main bundle.
const PromptCodeEditor = lazy(() => import('./promptEditor/PromptCodeEditor'));

interface MarkdownPromptEditorProps {
  value: string;
  onChange: (value: string) => void;
  label?: string;
  placeholder?: string;
  helperText?: ReactNode;
  /** Liquid variables the autocomplete offers after `{{` (#87). */
  variables?: readonly Completion[];
  /** The profile this prompt belongs to: backend-aware suggestions and reference checks (#123). */
  profile?: PromptProfileContext;
}

export function MarkdownPromptEditor({
  value,
  onChange,
  label = 'Prompt',
  placeholder = 'Write the profile instructions in Markdown. Liquid variables are rendered at runtime.',
  helperText,
  variables = PROFILE_VARIABLES,
  profile,
}: MarkdownPromptEditorProps) {
  const [tab, setTab] = useState<'write' | 'preview'>('write');
  const deferredValue = useDeferredValue(value);

  const preview = useMemo(() => deferredValue.trim(), [deferredValue]);

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-3">
        <label className={fieldLabelCls}>{label}</label>
        <div className="border-theme-line bg-theme-bg-soft inline-flex rounded-[var(--radius-sm)] border p-0.5">
          {(['write', 'preview'] as const).map((nextTab) => (
            <button
              key={nextTab}
              type="button"
              onClick={() => {
                setTab(nextTab);
              }}
              className={`rounded-[calc(var(--radius-sm)-2px)] px-3 py-1 text-[11px] font-medium transition-colors ${
                tab === nextTab
                  ? 'bg-theme-accent text-white'
                  : 'text-theme-text-secondary hover:text-theme-text'
              }`}
            >
              {nextTab === 'write' ? 'Write' : 'Preview'}
            </button>
          ))}
        </div>
      </div>

      {tab === 'write' ? (
        <Suspense
          fallback={
            // Shows the text read-only while the editor chunk loads.
            <textarea
              readOnly
              aria-busy="true"
              aria-label={`${label} (loading editor)`}
              value={value}
              className={`${textareaCls} min-h-[420px] text-[13px] leading-6`}
            />
          }
        >
          <PromptCodeEditor
            value={value}
            onChange={onChange}
            label={label}
            variables={variables}
            placeholder={placeholder}
            profile={profile}
          />
        </Suspense>
      ) : (
        <div className="border-theme-line bg-theme-panel min-h-[420px] rounded-[var(--radius-sm)] border p-4">
          {preview ? (
            <div className={proseClass}>
              <ReactMarkdown remarkPlugins={[remarkGfm]}>{preview}</ReactMarkdown>
            </div>
          ) : (
            <p className="text-theme-muted text-sm">Nothing to preview yet.</p>
          )}
        </div>
      )}

      <p className={helperTextCls}>
        {helperText ?? (
          <>
            Profile prompts are rendered with Liquid before each run. Use plain Markdown for
            structure and <span className="font-mono">{'{{ issue.* }}'}</span> variables for
            issue-specific data. Type <span className="font-mono">{'{{'}</span> for variables,{' '}
            <span className="font-mono">/</span> or <span className="font-mono">@</span> for the
            skills and subagents this profile&apos;s backend can use.
          </>
        )}
      </p>
    </div>
  );
}
