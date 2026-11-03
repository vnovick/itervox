/**
 * #214 — token scopes. The daemon enforces them on every route (a refused
 * call answers 403 insufficient_scope); the dashboard only uses them to hide
 * controls the caller's scope cannot use.
 */
export type Scope = 'viewer' | 'operator' | 'admin';

export const SCOPES: readonly Scope[] = ['viewer', 'operator', 'admin'];

const RANK: Record<Scope, number> = { viewer: 1, operator: 2, admin: 3 };

/**
 * The scope a snapshot reports. A daemon predating scopes omits the field
 * and treats every caller as admin, so absent means admin; an unknown value
 * is treated as the least privileged scope.
 */
export function normalizeScope(raw: string | undefined): Scope {
  if (raw === undefined) return 'admin';
  return (SCOPES as readonly string[]).includes(raw) ? (raw as Scope) : 'viewer';
}

/** Whether a caller holding `have` may use something that needs `need`. */
export function scopeAllows(have: Scope, need: Scope): boolean {
  return RANK[have] >= RANK[need];
}

/** Header the daemon sets on a 403 insufficient_scope response. */
export const REQUIRED_SCOPE_HEADER = 'X-Itervox-Required-Scope';
