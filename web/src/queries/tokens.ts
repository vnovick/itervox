/**
 * #214 — stored API tokens: GET/POST /api/v1/tokens, DELETE
 * /api/v1/tokens/{id}, POST /api/v1/tokens/{id}/rotate. Admin only (the
 * daemon refuses other scopes with 403 insufficient_scope). Token lists are
 * TanStack Query server state; a created or rotated secret is returned to
 * the caller once and never cached.
 */
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiRequest } from '../auth/apiRequest';
import { UnauthorizedError } from '../auth/UnauthorizedError';
import type { Scope } from '../auth/scope';
import { useToastStore } from '../store/toastStore';
import {
  ApiTokenListSchema,
  ApiTokenSecretSchema,
  type ApiToken,
  type ApiTokenSecret,
} from '../types/schemas';

export const API_TOKENS_KEY = ['apiTokens'] as const;

export function useApiTokens(enabled: boolean) {
  return useQuery({
    queryKey: API_TOKENS_KEY,
    queryFn: async (): Promise<ApiToken[]> => {
      const { data } = await apiRequest('/api/v1/tokens', { op: 'token list' });
      return ApiTokenListSchema.parse(data).tokens;
    },
    enabled,
    staleTime: 10_000,
  });
}

export interface NewApiToken {
  name: string;
  scope: Scope;
  /** e.g. "90d"; empty for no expiry. */
  expires: string;
}

function reportError(prefix: string, err: unknown) {
  if (err instanceof UnauthorizedError) return;
  const message = err instanceof Error ? err.message : 'Unexpected error';
  useToastStore.getState().addToast(`${prefix}: ${message}`, 'error');
}

export function useCreateApiToken() {
  const qc = useQueryClient();
  return useMutation<ApiTokenSecret, Error, NewApiToken>({
    mutationFn: async (vars) => {
      const { data } = await apiRequest('/api/v1/tokens', {
        op: 'token create',
        method: 'POST',
        json: vars,
      });
      return ApiTokenSecretSchema.parse(data);
    },
    onError: (err) => {
      reportError('Creating the token failed', err);
    },
    onSettled: () => qc.invalidateQueries({ queryKey: API_TOKENS_KEY }),
  });
}

export function useRotateApiToken() {
  const qc = useQueryClient();
  return useMutation<ApiTokenSecret, Error, string>({
    mutationFn: async (id) => {
      const { data } = await apiRequest(`/api/v1/tokens/${encodeURIComponent(id)}/rotate`, {
        op: 'token rotate',
        method: 'POST',
      });
      return ApiTokenSecretSchema.parse(data);
    },
    onError: (err) => {
      reportError('Rotating the token failed', err);
    },
    onSettled: () => qc.invalidateQueries({ queryKey: API_TOKENS_KEY }),
  });
}

export function useRevokeApiToken() {
  const qc = useQueryClient();
  return useMutation<string, Error, string, { previous: ApiToken[] | undefined }>({
    mutationFn: async (id) => {
      await apiRequest(`/api/v1/tokens/${encodeURIComponent(id)}`, {
        op: 'token revoke',
        method: 'DELETE',
      });
      return id;
    },
    onMutate: async (id) => {
      await qc.cancelQueries({ queryKey: API_TOKENS_KEY });
      const previous = qc.getQueryData<ApiToken[]>(API_TOKENS_KEY);
      qc.setQueryData<ApiToken[]>(API_TOKENS_KEY, (list) => list?.filter((t) => t.id !== id));
      return { previous };
    },
    onError: (err, _id, ctx) => {
      qc.setQueryData(API_TOKENS_KEY, ctx?.previous);
      reportError('Revoking the token failed', err);
    },
    onSettled: () => qc.invalidateQueries({ queryKey: API_TOKENS_KEY }),
  });
}
