import { useEffect, useState } from 'react';

interface ReviewerCardProps {
  reviewerProfile: string;
  autoReview: boolean;
  autoClearWorkspace: boolean;
  availableProfiles: string[];
  onSave: (profile: string, autoReview: boolean) => Promise<boolean>;
}

/** Local edits that override props while the user is making changes. */
interface PendingEdits {
  profile: string;
  auto: boolean;
}

export function ReviewerCard({
  reviewerProfile,
  autoReview,
  autoClearWorkspace,
  availableProfiles,
  onSave,
}: ReviewerCardProps) {
  const [pending, setPending] = useState<PendingEdits | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  // Derive display values: pending edits win over props.
  const profile = pending ? pending.profile : reviewerProfile;
  const auto = pending ? pending.auto : autoReview;
  const dirty = pending !== null;

  const updatePending = (patch: Partial<PendingEdits>) => {
    setError('');
    const base: PendingEdits = pending ?? { profile: reviewerProfile, auto: autoReview };
    const next = { ...base, ...patch };
    // If the edited values match props again, clear pending state.
    if (next.profile === reviewerProfile && next.auto === autoReview) {
      setPending(null);
    } else {
      setPending(next);
    }
  };

  useEffect(() => {
    if (!autoClearWorkspace) {
      setError('');
    }
  }, [autoClearWorkspace]);

  const handleSave = async () => {
    setSaving(true);
    try {
      const ok = await onSave(profile, auto);
      if (ok) {
        // After save, props will update via refreshSnapshot; clear local edits.
        setPending(null);
        setError('');
      } else {
        setError('Failed to save reviewer settings. Please try again.');
      }
    } catch {
      setError('Failed to save reviewer settings. Please try again.');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="space-y-4">
      <div>
        <label className="text-theme-text-secondary mb-1 block text-xs font-medium">
          Reviewer profile
        </label>
        <select
          value={profile}
          onChange={(e) => {
            const nextProfile = e.target.value;
            updatePending({ profile: nextProfile, ...(nextProfile === '' ? { auto: false } : {}) });
          }}
          className="w-full cursor-pointer rounded-[var(--radius-sm)] border border-[var(--line)] bg-[var(--panel-strong)] px-3 py-2 text-[13px] text-[var(--text)] focus:outline-none"
        >
          <option value="">None (disabled)</option>
          {availableProfiles.map((p) => (
            <option key={p} value={p}>
              {p}
            </option>
          ))}
        </select>
        <p className="text-theme-muted mt-1 text-[10px]">
          Select a profile to use for AI code review. The reviewer runs as a regular worker with the
          profile's command, backend, and prompt.
        </p>
      </div>

      <label className="flex cursor-pointer items-center gap-2">
        <input
          type="checkbox"
          checked={auto}
          onChange={(e) => {
            if (e.target.checked && autoClearWorkspace) {
              setError(
                'Auto-review cannot be enabled while auto-clear workspace is enabled. Disable auto-clear first.',
              );
              return;
            }
            updatePending({ auto: e.target.checked });
          }}
          disabled={!profile}
          className="border-theme-line rounded"
        />
        <span className={`text-sm ${!profile ? 'text-theme-muted' : 'text-theme-text'}`}>
          Auto-review after agent succeeds
        </span>
      </label>
      {error && (
        <p role="alert" className="text-theme-danger pl-6 text-[10px]">
          {error}
        </p>
      )}
      {auto && profile && (
        <p className="text-theme-muted pl-6 text-[10px]">
          A reviewer worker will be automatically dispatched using the <strong>{profile}</strong>{' '}
          profile after each successful agent run.
        </p>
      )}

      {dirty && (
        <button
          onClick={() => {
            void handleSave();
          }}
          disabled={saving}
          className="bg-theme-accent rounded-[var(--radius-sm)] px-4 py-2 text-sm font-medium text-white transition-colors disabled:opacity-50"
        >
          {saving ? 'Saving…' : 'Save'}
        </button>
      )}
    </div>
  );
}
