// Rules editor queries (#157).
//
// Rules are files the daemon re-reads for every run, not part of the SSE
// snapshot, so they are TanStack Query server state: `useRules` reads every
// layer, `useRulesPreview` the effective rules for a profile/issue, and
// `useSaveRulesLayer` writes one layer, then puts the saved layer in the
// cache and refetches the preview. All requests go through apiRequest
// (authedFetch underneath).

import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ApiError, apiRequest } from '../auth/apiRequest';
import { UnauthorizedError } from '../auth/UnauthorizedError';
import { useToastStore } from '../store/toastStore';
import {
  RulesLayerSchema,
  RulesPreviewSchema,
  RulesViewSchema,
  type Rule,
  type RulesLayer,
  type RulesLayerName,
  type RulesPreview,
  type RulesView,
} from '../types/schemas';

export const RULES_KEY = ['rules'] as const;
export const RULES_PREVIEW_KEY = ['rules', 'preview'] as const;

export function useRules() {
  return useQuery({
    queryKey: RULES_KEY,
    queryFn: async (): Promise<RulesView> => {
      const { data } = await apiRequest('/api/v1/rules', { op: 'rules fetch' });
      return RulesViewSchema.parse(data);
    },
  });
}

export function useRulesPreview(profile: string, issue: string) {
  return useQuery({
    queryKey: [...RULES_PREVIEW_KEY, profile, issue],
    queryFn: async (): Promise<RulesPreview> => {
      const params = new URLSearchParams();
      if (profile) params.set('profile', profile);
      if (issue) params.set('issue', issue);
      const qs = params.toString();
      const { data } = await apiRequest(`/api/v1/rules/preview${qs ? `?${qs}` : ''}`, {
        op: 'rules preview',
      });
      return RulesPreviewSchema.parse(data);
    },
    placeholderData: keepPreviousData,
  });
}

/** One save: the JSON view sends `raw`, the form view sends `rules`. */
export interface SaveRulesInput {
  layer: RulesLayerName;
  baseVersion: string;
  raw?: string;
  rules?: Rule[];
}

export function useSaveRulesLayer() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (input: SaveRulesInput): Promise<RulesLayer> => {
      const body =
        input.raw !== undefined
          ? { baseVersion: input.baseVersion, raw: input.raw }
          : { baseVersion: input.baseVersion, rules: input.rules ?? [] };
      const { data } = await apiRequest(`/api/v1/rules/${input.layer}`, {
        op: 'rules save',
        method: 'PUT',
        json: body,
      });
      return RulesLayerSchema.parse(data);
    },
    onSuccess: (layer) => {
      qc.setQueryData<RulesView>(RULES_KEY, (prev) =>
        prev
          ? { ...prev, layers: prev.layers.map((l) => (l.name === layer.name ? layer : l)) }
          : prev,
      );
      void qc.invalidateQueries({ queryKey: RULES_PREVIEW_KEY });
      useToastStore.getState().addToast(`Saved the ${layer.name} rules`, 'success');
    },
    onError: (err) => {
      if (err instanceof UnauthorizedError) return;
      if (err instanceof ApiError && err.status === 409) {
        // Someone else changed the file: show what is there now.
        void qc.invalidateQueries({ queryKey: RULES_KEY });
        useToastStore
          .getState()
          .addToast(
            'The rules file changed since it was loaded. Reloaded it; apply your edit again.',
            'error',
          );
        return;
      }
      const message = err instanceof Error ? err.message : 'Unexpected error';
      useToastStore.getState().addToast(`Rules not saved: ${message}`, 'error');
    },
  });
}
