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
    <div className="border-theme-line bg-theme-bg-elevated overflow-hidden rounded-[var(--radius-md)] border">
      <div className="border-theme-line bg-theme-panel-strong border-b px-5 py-4">
        <h2 className="text-theme-text text-sm font-semibold">Workspace</h2>
      </div>
      <div className="px-5 py-5">
        <label className="flex cursor-pointer items-start gap-4">
          <div className="relative mt-0.5 flex-shrink-0">
            <input
              type="checkbox"
              className="sr-only"
              checked={autoClearWorkspace}
              disabled={saving}
              onChange={(e) => {
                void handleChange(e.target.checked);
              }}
            />
            <div
              aria-hidden="true"
              className="h-5 w-9 rounded-full transition-colors"
              style={{ background: autoClearWorkspace ? 'var(--accent)' : 'var(--bg-soft)' }}
            />
            <div
              aria-hidden="true"
              className={`absolute top-0.5 h-4 w-4 rounded-full bg-white shadow transition-transform ${
                autoClearWorkspace ? 'translate-x-4' : 'translate-x-0.5'
              }`}
            />
          </div>
          <div>
            <span className="text-theme-text block text-sm font-medium">
              Auto-clear workspace on success
            </span>
            <span className="text-theme-text-secondary mt-0.5 block text-xs">
              When a task completes successfully (reaches the completion state), automatically
              delete the cloned workspace directory. Logs are always kept for visibility.
            </span>
            {visibleError && (
              <span role="alert" className="text-theme-danger mt-1 block text-xs">
                {visibleError}
              </span>
            )}
          </div>
        </label>
      </div>
    </div>
  );
}
