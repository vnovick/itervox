import { useState } from 'react';
import { useTokenStore } from './tokenStore';
import { useAuthStore } from './authStore';
import { authProbeUrl, getApiContext } from './apiBase';
import { StatusCard, StatusScreen } from '../components/common/StatusCard';
import { plainButtonClass } from '../components/ui/button/buttonRecipe';
import { FIELD_CLASS } from '../components/ui/Input/fieldClass';

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
      const res = await fetch(authProbeUrl(), {
        headers: { Authorization: `Bearer ${trimmed}` },
      });
      if (res.status === 401) {
        setError(
          getApiContext().mode === 'daemon'
            ? 'Token rejected by server (401). Check your ITERVOX_API_TOKEN.'
            : 'Hub token rejected (401). Create one with: itervox token create --hub --name NAME --scope viewer',
        );
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
    <StatusScreen>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void submitToken();
        }}
        className="w-full max-w-[440px]"
      >
        <StatusCard as="div" brand title="Sign in to Itervox" className="max-w-none">
          <p>
            This dashboard requires an API token. Paste the value of{' '}
            <code className="text-theme-text font-mono text-[13px] font-semibold">
              ITERVOX_API_TOKEN
            </code>{' '}
            below, or open the URL printed in the daemon startup banner (it carries the token).
          </p>

          <div>
            <label htmlFor="api-token" className="field-label">
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
              className={`${FIELD_CLASS} h-11 font-mono`}
              placeholder="paste token here"
            />
          </div>

          <label className="flex items-center gap-2 text-[13px]">
            <input
              type="checkbox"
              className="accent-theme-accent"
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
              className="bg-theme-danger-soft text-theme-danger-text rounded-[var(--radius-sm)] px-3 py-2.5 text-[13px]"
            >
              {error}
            </div>
          )}

          <button
            type="submit"
            disabled={submitting}
            className={plainButtonClass('primary', 'lg', { extra: 'w-full' })}
          >
            {submitting ? 'Validating…' : 'Sign in'}
          </button>
        </StatusCard>
      </form>
    </StatusScreen>
  );
}
