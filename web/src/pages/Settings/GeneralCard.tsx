// GeneralCard — top of /settings; one-toggle card for the cross-cutting
// behavioral knobs that don't belong anywhere else (today: `inline_input`).
// Keeping it as the first card so first-time operators see their two
// fundamental choices about agent input handling before they hit
// state-machine config.
//
// Toggle visuals + a11y wiring mirror WorkspaceCard exactly so the two
// cards feel like a pair (label-wraps-checkbox, sr-only input, animated
// track + thumb). Anything that diverges between them should be moved into
// a shared primitive.

import { useState } from 'react';

interface GeneralCardProps {
  inlineInput: boolean;
  onSetInlineInput: (enabled: boolean) => Promise<boolean>;
}

export function GeneralCard({ inlineInput, onSetInlineInput }: GeneralCardProps) {
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  const handleChange = async (enabled: boolean) => {
    if (saving) return;
    setSaving(true);
    setError('');
    const ok = await onSetInlineInput(enabled);
    setSaving(false);
    if (!ok) setError('Failed to save inline-input setting. Please try again.');
  };

  return (
    <div className="border-theme-line bg-theme-bg-elevated overflow-hidden rounded-[var(--radius-md)] border">
      <div className="border-theme-line bg-theme-panel-strong border-b px-5 py-4">
        <h2 className="text-theme-text text-sm font-semibold">General</h2>
      </div>
      <div className="px-5 py-5">
        <label className="flex cursor-pointer items-start gap-4">
          <div className="relative mt-0.5 flex-shrink-0">
            <input
              type="checkbox"
              className="sr-only"
              checked={inlineInput}
              disabled={saving}
              onChange={(e) => {
                void handleChange(e.target.checked);
              }}
            />
            <div
              aria-hidden="true"
              className="h-5 w-9 rounded-full transition-colors"
              style={{ background: inlineInput ? 'var(--accent)' : 'var(--bg-soft)' }}
            />
            <div
              aria-hidden="true"
              className={`absolute top-0.5 h-4 w-4 rounded-full bg-white shadow transition-transform ${
                inlineInput ? 'translate-x-4' : 'translate-x-0.5'
              }`}
            />
          </div>
          <div>
            <span className="text-theme-text block text-sm font-medium">
              Inline input via tracker comments
            </span>
            <span className="text-theme-text-secondary mt-0.5 block text-xs leading-relaxed">
              When an agent needs human input mid-run:
              <br />
              <span className="font-semibold">Off (default):</span> the question surfaces in the
              dashboard&rsquo;s &ldquo;Resuming&rdquo; panel — you reply from the dashboard, the
              agent resumes.
              <br />
              <span className="font-semibold">On:</span> the daemon posts the question as a comment
              on the tracker issue and moves the issue to the completion state. You reply in the
              tracker (Linear / GitHub) and move the issue back to active to resume.
            </span>
            {error && (
              <span role="alert" className="text-theme-danger mt-1 block text-xs">
                {error}
              </span>
            )}
          </div>
        </label>
      </div>
    </div>
  );
}
