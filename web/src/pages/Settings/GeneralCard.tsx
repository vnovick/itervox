// GeneralCard — top of /settings; one-toggle card for the cross-cutting
// behavioral knobs that don't belong anywhere else (today: `inline_input`).
// Keeping it as the first card so first-time operators see their two
// fundamental choices about agent input handling before they hit
// state-machine config.
//
// The toggle is the shared `.switch` (a native checkbox drawn as a switch),
// wrapped by its label, exactly like WorkspaceCard.

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
    <div>
      <div>
        <label className="flex cursor-pointer items-start justify-between gap-4">
          <input
            type="checkbox"
            className="switch order-last mt-0.5"
            checked={inlineInput}
            disabled={saving}
            onChange={(e) => {
              void handleChange(e.target.checked);
            }}
          />
          <div className="min-w-0 flex-1">
            <span className="text-theme-text block text-sm font-medium">
              Inline input via tracker comments
            </span>
            <span className="text-theme-text-secondary mt-0.5 block text-xs leading-relaxed">
              When an agent needs human input mid-run:
              <br />
              <span className="font-semibold">Off (default):</span> the question is posted on the
              tracker issue and also shown in the dashboard&rsquo;s &ldquo;Resuming&rdquo; panel —
              reply from either place and the agent resumes.
              <br />
              <span className="font-semibold">On:</span> the tracker is the only place to reply. The
              dashboard reply box is hidden; comment on the issue in Linear / GitHub and the agent
              resumes.
            </span>
            {error && (
              <span role="alert" className="text-theme-danger-text mt-1 block text-xs">
                {error}
              </span>
            )}
          </div>
        </label>
      </div>
    </div>
  );
}
