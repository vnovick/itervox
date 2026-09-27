import { useState } from 'react';
import { useTokenStore } from './tokenStore';
import { useAuthStore } from './authStore';

/**
 * Shown when the server requires auth but we have no valid token.
 *
 * On submit: validates by calling /api/v1/state with the candidate token as
 * a Bearer header. If ok, persists per the "Remember" checkbox and flips
 * auth status to 'authorized'. If 401, shows an inline error.
 */
export function TokenEntryScreen() {
  const rejectedReason = useAuthStore((s) => s.rejectedReason);
  const setToken = useTokenStore((s) => s.setToken);
  const setStatus = useAuthStore((s) => s.setStatus);

  const [value, setValue] = useState('');
  const [remember, setRemember] = useState(false);
  const [error, setError] = useState<string | null>(rejectedReason);
  const [submitting, setSubmitting] = useState(false);

  async function submitToken() {
    const trimmed = value.trim();
    if (!trimmed) {
      setError('Paste your API token above.');
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      // Validate directly with fetch — we can't use authedFetch here because
      // the token isn't stored yet and we don't want to mutate global state
      // on a bad submission.
      const res = await fetch('/api/v1/state', {
        headers: { Authorization: `Bearer ${trimmed}` },
      });
      if (res.status === 401) {
        setError('Token rejected by server (401). Check your ITERVOX_API_TOKEN.');
        return;
      }
      if (!res.ok) {
        setError(`Unexpected server response: ${String(res.status)}`);
        return;
      }
      setToken(trimmed, remember);
      setStatus('authorized');
    } catch {
      setError('Network error — is the daemon still running?');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center p-6">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void submitToken();
        }}
        className="bg-theme-bg-soft border-theme-line w-full max-w-md rounded-[var(--radius-lg)] border p-6 shadow-xl"
      >
        <h1 className="mb-2 text-xl font-semibold">Sign in to Itervox</h1>
        <p className="text-theme-muted mb-4 text-sm">
          This dashboard requires an API token. Paste the value of{' '}
          <code className="font-mono text-xs">ITERVOX_API_TOKEN</code> below, or open the URL
          printed in the daemon startup banner (it carries the token).
        </p>

        <label htmlFor="api-token" className="mb-1 block text-sm font-medium">
          API token
        </label>
        <input
          id="api-token"
          type="password"
          autoComplete="current-password"
          // eslint-disable-next-line jsx-a11y/no-autofocus -- the token field is the only control on this screen; focusing it is the expected login behaviour
          autoFocus
          value={value}
          onChange={(e) => {
            setValue(e.target.value);
          }}
          className="bg-theme-bg border-theme-line w-full rounded-[var(--radius-md)] border px-3 py-2 font-mono text-sm outline-none focus:border-[color:var(--color-accent)]"
          placeholder="paste token here"
        />

        <label className="mt-3 flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={remember}
            onChange={(e) => {
              setRemember(e.target.checked);
            }}
          />
          Remember on this device (stores token in localStorage)
        </label>

        {error && (
          <div
            role="alert"
            className="mt-3 rounded-[var(--radius-sm)] border border-red-400/40 bg-red-500/10 px-3 py-2 text-sm text-red-400"
          >
            {error}
          </div>
        )}

        <button
          type="submit"
          disabled={submitting}
          className="mt-4 w-full rounded-[var(--radius-md)] bg-[color:var(--color-accent)] px-4 py-2 text-sm font-medium text-white disabled:opacity-60"
        >
          {submitting ? 'Validating…' : 'Sign in'}
        </button>
      </form>
    </div>
  );
}
