import { useState } from 'react';
import { SCOPES, type Scope } from '../../auth/scope';
import type { NewApiToken } from '../../queries/tokens';
import { fieldLabelCls, helperTextCls, inputCls, selectCls } from './formStyles';

const SCOPE_HELP: Record<Scope, string> = {
  viewer: 'read-only: state, logs and streams, secrets redacted',
  operator: 'adds dispatch, steering, input replies, approvals, pause/resume, memory',
  admin: 'everything, including settings, tokens and tmux control',
};

/** #214 — the "New token" form of Settings → Access. */
export function AccessTokenForm({
  pending,
  onCreate,
}: {
  pending: boolean;
  onCreate: (vars: NewApiToken) => void;
}) {
  const [name, setName] = useState('');
  const [scope, setScope] = useState<Scope>('viewer');
  const [expires, setExpires] = useState('');
  const trimmed = name.trim();

  return (
    <form
      className="grid gap-3 sm:grid-cols-[2fr_1fr_1fr_auto] sm:items-end"
      onSubmit={(e) => {
        e.preventDefault();
        if (trimmed === '' || pending) return;
        onCreate({ name: trimmed, scope, expires: expires.trim() });
        setName('');
        setExpires('');
      }}
    >
      <label className="block">
        <span className={fieldLabelCls}>Name</span>
        <input
          className={inputCls}
          value={name}
          maxLength={64}
          placeholder="e.g. teammate laptop"
          onChange={(e) => {
            setName(e.target.value);
          }}
        />
      </label>
      <label className="block">
        <span className={fieldLabelCls}>Scope</span>
        <select
          className={selectCls}
          value={scope}
          onChange={(e) => {
            setScope(e.target.value as Scope);
          }}
        >
          {SCOPES.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
      </label>
      <label className="block">
        <span className={fieldLabelCls}>Expires</span>
        <input
          className={inputCls}
          value={expires}
          placeholder="never (or 90d, 2027-01-31)"
          onChange={(e) => {
            setExpires(e.target.value);
          }}
        />
      </label>
      <button
        type="submit"
        disabled={trimmed === '' || pending}
        className="bg-theme-accent rounded-[var(--radius-sm)] px-3 py-2 text-[13px] font-medium text-white disabled:opacity-50"
      >
        {pending ? 'Creating…' : 'Create token'}
      </button>
      <p className={`${helperTextCls} sm:col-span-4`}>
        {scope}: {SCOPE_HELP[scope]}.
      </p>
    </form>
  );
}
