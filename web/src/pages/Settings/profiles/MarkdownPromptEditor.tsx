import { useDeferredValue, useMemo, useState, type ReactNode } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { proseClass } from '../../../utils/format';
import { fieldLabelCls, helperTextCls, textareaCls } from '../formStyles';

interface MarkdownPromptEditorProps {
  value: string;
  onChange: (value: string) => void;
  label?: string;
  placeholder?: string;
  helperText?: ReactNode;
}

export function MarkdownPromptEditor({
  value,
  onChange,
  label = 'Prompt',
  placeholder = 'Write the profile instructions in Markdown. Liquid variables are rendered at runtime.',
  helperText,
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
        <textarea
          value={value}
          onChange={(event) => {
            onChange(event.target.value);
          }}
          placeholder={placeholder}
          className={`${textareaCls} min-h-[420px] text-[13px] leading-6`}
        />
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
            issue-specific data.
          </>
        )}
      </p>
    </div>
  );
}
