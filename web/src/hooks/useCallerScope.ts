import { useItervoxStore } from '../store/itervoxStore';
import { normalizeScope, scopeAllows, type Scope } from '../auth/scope';

/**
 * #214 — the caller's token scope, from the snapshot. Before the first
 * snapshot arrives it is 'viewer' (show nothing that needs more); after,
 * an absent field means a daemon predating scopes, i.e. admin.
 */
export function useCallerScope(): Scope {
  return useItervoxStore((s) =>
    s.snapshot === null ? 'viewer' : normalizeScope(s.snapshot.callerScope),
  );
}

/** Whether the caller's scope allows `need`. */
export function useScopeAllows(need: Scope): boolean {
  return scopeAllows(useCallerScope(), need);
}
