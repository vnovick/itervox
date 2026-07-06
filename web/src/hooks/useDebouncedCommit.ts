// Gap §3.4 — shared commit-on-blur hook for settings inputs whose value
// is derived from server snapshot but should only fire the PUT on user
// interaction, not on every keystroke.
//
// Pattern: each input has (a) a draft string the user types into and
// (b) a settled value from the server snapshot. The hook owns the draft,
// re-syncs when the server value changes, and exposes a `commit` callback
// that validates and fires the setter on blur.
//
// Used by RetriesCard + SwitchCapSection — replaces the four near-identical
// commit-on-blur handlers documented in gaps_010526 §3.4.

import { useEffect, useState } from 'react';

export interface UseDebouncedCommitOptions<T> {
  // Server-side settled value. The hook re-syncs the draft when this changes.
  value: T;
  // Setter the hook fires on commit. Returns true on success, false on
  // failure — failure reverts the draft to the server value.
  setter: (next: T) => Promise<boolean>;
  // Validate + transform the draft before calling setter. Return
  //   { ok: true, value: T } to commit
  //   { ok: false, error: string } to reject (sets error + reverts draft)
  parse: (draft: string) => { ok: true; value: T } | { ok: false; error: string };
  // Render the settled value as a string for the draft. Defaults to String().
  // NOTE: deliberately named `serialize` rather than `toString`. `toString`
  // collides with `Object.prototype.toString`, so a destructure with default
  // would never see "undefined" — it would always receive the inherited
  // built-in, producing "[object Number]" / "[object Undefined]" garbage.
  serialize?: (v: T) => string;
}

export interface UseDebouncedCommitResult {
  draft: string;
  setDraft: (next: string) => void;
  commit: () => Promise<void>;
  saving: boolean;
  error: string;
}

export function useDebouncedCommit<T>(
  opts: UseDebouncedCommitOptions<T>,
): UseDebouncedCommitResult {
  const { value, setter, parse } = opts;
  const stringify = opts.serialize ?? defaultToString;

  const [draft, setDraft] = useState<string>(() => stringify(value));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  // Re-sync when the server value flips (snapshot tick or other-tab edit).
  useEffect(() => {
    setDraft(stringify(value));
    // stringify is intentionally not in the dep array — callers pass a
    // stable function; including it would re-fire on every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value]);

  const commit = async () => {
    const parsed = parse(draft);
    if (!parsed.ok) {
      setError(parsed.error);
      setDraft(stringify(value));
      return;
    }
    if (parsed.value === value) {
      // No change — clear any prior error, no PUT.
      setError('');
      return;
    }
    setSaving(true);
    setError('');
    const ok = await setter(parsed.value);
    setSaving(false);
    if (!ok) {
      setError('Failed to save. Please try again.');
      setDraft(stringify(value));
    }
  };

  return { draft, setDraft, commit, saving, error };
}

function defaultToString(v: unknown): string {
  return String(v);
}
