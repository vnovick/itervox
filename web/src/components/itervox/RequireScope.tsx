import type { ReactNode } from 'react';
import type { Scope } from '../../auth/scope';
import { useScopeAllows } from '../../hooks/useCallerScope';

/**
 * #214 — renders its children only when the caller's token scope allows
 * `scope` (else `fallback`). Cosmetic: the daemon enforces scopes on every
 * route regardless.
 */
export function RequireScope({
  scope,
  children,
  fallback = null,
}: {
  scope: Scope;
  children: ReactNode;
  fallback?: ReactNode;
}) {
  return useScopeAllows(scope) ? <>{children}</> : <>{fallback}</>;
}
