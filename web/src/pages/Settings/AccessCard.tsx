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

const EMPTY_TOKENS: readonly ApiToken[] = [];

function day(iso: string | undefined): string {
  return iso ? iso.slice(0, 10) : '—';
}

function NewSecret({ issued, onDone }: { issued: ApiTokenSecret; onDone: () => void }) {
  return (
    <div
      role="status"
      data-testid="access-new-secret"
      className="border-theme-accent bg-theme-bg-soft space-y-2 rounded-[var(--radius-sm)] border p-3"
    >
      <p className="text-theme-text text-sm font-medium">
        Token “{issued.token.name}” ({issued.token.scope}) — copy it now, it is shown only once.
      </p>
      <div className="flex items-center gap-2">
        <code className="bg-theme-panel-strong flex-1 overflow-x-auto rounded px-2 py-1 font-mono text-xs">
          {issued.secret}
        </code>
        <CopyButton text={issued.secret} label="Copy token" />
        <button type="button" className="text-theme-muted text-xs underline" onClick={onDone}>
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
    <div className="border-theme-line bg-theme-bg-elevated overflow-hidden rounded-[var(--radius-md)] border">
      <div className="border-theme-line bg-theme-panel-strong border-b px-5 py-4">
        <h2 className="text-theme-text text-sm font-semibold">API tokens</h2>
        <p className="text-theme-muted mt-1 text-xs">
          Named viewer, operator and admin tokens, stored as hashes in .itervox/tokens.json.
          Revoking or rotating takes effect within a second, without a restart. The
          ITERVOX_API_TOKEN / auto-generated token is always admin and is not listed.
        </p>
      </div>
      <div className="space-y-4 px-5 py-5">
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
          <table className="w-full text-left text-[13px]" aria-label="API tokens">
            <thead className="text-theme-muted text-xs">
              <tr>
                <th className="py-1 font-medium">Name</th>
                <th className="py-1 font-medium">Scope</th>
                <th className="py-1 font-medium">Created</th>
                <th className="py-1 font-medium">Last used</th>
                <th className="py-1 font-medium">Expires</th>
                <th className="py-1" />
              </tr>
            </thead>
            <tbody>
              {list.map((t) => (
                <tr
                  key={t.id}
                  className="border-theme-line border-t"
                  data-testid="access-token-row"
                >
                  <td className="py-2">
                    <span className="text-theme-text">{t.name}</span>{' '}
                    <span className="text-theme-muted font-mono text-[11px]">{t.id}</span>
                  </td>
                  <td className="py-2">{t.scope}</td>
                  <td className="py-2">{day(t.created)}</td>
                  <td className="py-2">{day(t.lastUsed)}</td>
                  <td className="py-2">
                    {t.expires ? day(t.expires) : 'never'}
                    {t.expired ? ' (expired)' : ''}
                  </td>
                  <td className="flex justify-end gap-2 py-2">
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
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}
