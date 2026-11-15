import { useState } from 'react';

const AUTO_REVIEW_CONFLICT_ERROR =
  'Auto-clear cannot be enabled while auto-review is enabled. Disable auto-review first.';

interface WorkspaceCardProps {
  autoClearWorkspace: boolean;
  autoReviewEnabled: boolean;
  onToggle: (enabled: boolean) => Promise<boolean>;
}

export function WorkspaceCard({
  autoClearWorkspace,
  autoReviewEnabled,
  onToggle,
}: WorkspaceCardProps) {
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const visibleError = !autoReviewEnabled && error === AUTO_REVIEW_CONFLICT_ERROR ? '' : error;

  const handleChange = async (enabled: boolean) => {
    if (saving) return;
    if (enabled && autoReviewEnabled) {
      setError(AUTO_REVIEW_CONFLICT_ERROR);
      return;
    }
    setSaving(true);
    setError('');
    const ok = await onToggle(enabled);
    setSaving(false);
    if (!ok) setError('Failed to save workspace setting. Please try again.');
  };

  return (
    <div>
      <div>
        <label className="flex cursor-pointer items-start justify-between gap-4">
          <input
            type="checkbox"
            className="switch order-last mt-0.5"
            checked={autoClearWorkspace}
            disabled={saving}
            onChange={(e) => {
              void handleChange(e.target.checked);
            }}
          />
          <div className="min-w-0 flex-1">
            <span className="text-theme-text block text-sm font-medium">
              Auto-clear workspace on success
            </span>
            <span className="text-theme-text-secondary mt-0.5 block text-xs">
              When a task completes successfully (reaches the completion state), automatically
              delete the cloned workspace directory. Logs are always kept for visibility.
            </span>
            {visibleError && (
              <span role="alert" className="text-theme-danger-text mt-1 block text-xs">
                {visibleError}
              </span>
            )}
          </div>
        </label>
      </div>
    </div>
  );
}
