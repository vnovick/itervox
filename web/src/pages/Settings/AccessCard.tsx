// #214 — Settings → Access: the stored API tokens (.itervox/tokens.json).
// Admin only (the page renders it inside RequireScope; the daemon refuses
// other scopes anyway). A created or rotated secret is shown once, here,
// and never stored by the dashboard.

import { useState } from 'react';
import { CopyButton } from '../../components/ui/CopyButton';
import { ConfirmButton } from '../../components/ui/button/ConfirmButton';
import {
  useApiTokens,
  useCreateApiToken,
  useRevokeApiToken,
  useRotateApiToken,
} from '../../queries/tokens';
import type { ApiToken, ApiTokenSecret } from '../../types/schemas';
import { AccessTokenForm } from './AccessTokenForm';
import { buttonClass } from '../../components/ui/button/buttonStyles';

const EMPTY_TOKENS: readonly ApiToken[] = [];

function day(iso: string | undefined): string {
  return iso ? iso.slice(0, 10) : '—';
}

function NewSecret({ issued, onDone }: { issued: ApiTokenSecret; onDone: () => void }) {
  return (
    <div
      role="status"
      data-testid="access-new-secret"
      className="choice-card flex-col gap-2"
      data-active="true"
    >
      <p className="text-theme-text text-[13px]">
        Token “{issued.token.name}” ({issued.token.scope}) — copy it now, it is shown only once.
      </p>
      <code className="subtle-fill text-theme-text block overflow-x-auto rounded-[10px] px-2.5 py-2 font-mono text-xs">
        {issued.secret}
      </code>
      <div className="flex items-center gap-2">
        <code className="code-block flex-1 px-3 py-1.5">{issued.secret}</code>
        <CopyButton text={issued.secret} label="Copy token" />
        <button
          type="button"
          className={buttonClass('secondary', 'xs', undefined, true)}
          onClick={onDone}
        >
          Done
        </button>
      </div>
    </div>
  );
}

export function AccessCard() {
  const tokens = useApiTokens(true);
  const create = useCreateApiToken();
  const rotate = useRotateApiToken();
  const revoke = useRevokeApiToken();
  const [issued, setIssued] = useState<ApiTokenSecret | null>(null);
  const list = tokens.data ?? EMPTY_TOKENS;

  return (
    <div className="space-y-3">
      <div>
        <h3 className="text-theme-text text-sm font-semibold">API tokens</h3>
        <p className="text-theme-text-secondary mt-1 text-[13px]">
          Named viewer, operator and admin tokens, stored as hashes in{' '}
          <code className="font-mono">.itervox/tokens.json</code>. Revoking or rotating takes effect
          within a second, without a restart. The ITERVOX_API_TOKEN / auto-generated token is always
          admin and is not listed.
        </p>
      </div>
      <div className="space-y-3">
        {issued && (
          <NewSecret
            issued={issued}
            onDone={() => {
              setIssued(null);
            }}
          />
        )}
        <AccessTokenForm
          pending={create.isPending}
          onCreate={(vars) => {
            create.mutate(vars, { onSuccess: setIssued });
          }}
        />
        {tokens.isError && (
          <p className="text-theme-danger-text text-xs">
            Could not load tokens: {tokens.error.message}
          </p>
        )}
        {tokens.isSuccess && list.length === 0 && (
          <p className="text-theme-muted text-sm">No stored tokens yet.</p>
        )}
        {list.length > 0 && (
          <ul className="space-y-2" aria-label="API tokens">
            {list.map((t) => (
              <li
                key={t.id}
                className="glass-card flex flex-wrap items-center gap-x-2 gap-y-1 rounded-[14px] px-3 py-2.5 text-[13px]"
                data-testid="access-token-row"
              >
                <span className="text-theme-text font-semibold">{t.name}</span>
                <span className="text-theme-muted font-mono text-[11px]">{t.id}</span>
                <span className="text-theme-text-secondary flex flex-wrap gap-x-1.5">
                  <span>{t.scope}</span>
                  <span aria-hidden="true">·</span>
                  <span>
                    created <span>{day(t.created)}</span>
                  </span>
                  <span aria-hidden="true">·</span>
                  <span>
                    last used <span>{day(t.lastUsed)}</span>
                  </span>
                  <span aria-hidden="true">·</span>
                  <span>
                    expires <span>{t.expires ? day(t.expires) : 'never'}</span>
                    {t.expired ? ' (expired)' : ''}
                  </span>
                </span>
                <span className="ml-auto flex gap-1.5">
                  <ConfirmButton
                    label="Rotate"
                    ariaLabel={`Rotate ${t.name}`}
                    confirmLabel="Yes, rotate"
                    pendingLabel="Rotating…"
                    isPending={rotate.isPending && rotate.variables === t.id}
                    onConfirm={() => {
                      rotate.mutate(t.id, { onSuccess: setIssued });
                    }}
                  />
                  <ConfirmButton
                    label="Revoke"
                    ariaLabel={`Revoke ${t.name}`}
                    confirmLabel="Yes, revoke"
                    pendingLabel="Revoking…"
                    isPending={revoke.isPending && revoke.variables === t.id}
                    onConfirm={() => {
                      revoke.mutate(t.id);
                    }}
                  />
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
