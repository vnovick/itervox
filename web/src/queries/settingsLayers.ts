// Schema-driven Settings page queries (#158).
//
// The settings JSON Schema and the settings layer files are server state
// (TanStack Query), not part of the SSE snapshot. A save names its layer,
// puts the returned layers in the cache and then calls refreshSnapshot()
// (never patchSnapshot) so the dashboard shows the server's authoritative
// state, live settings included. All requests go through apiRequest
// (authedFetch underneath).

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ApiError, apiRequest } from '../auth/apiRequest';
import { UnauthorizedError } from '../auth/UnauthorizedError';
import { useItervoxStore } from '../store/itervoxStore';
import { useToastStore } from '../store/toastStore';
import {
  SettingsLayersViewSchema,
  SettingsSaveResultSchema,
  SettingsSchemaDocSchema,
  type SettingsLayerName,
  type SettingsLayersView,
  type SettingsSaveResult,
  type SettingsSchemaDoc,
} from '../types/schemas';

export const SETTINGS_SCHEMA_KEY = ['settings', 'schema'] as const;
export const SETTINGS_LAYERS_KEY = ['settings', 'layers'] as const;

export function useSettingsSchema() {
  return useQuery({
    queryKey: SETTINGS_SCHEMA_KEY,
    queryFn: async (): Promise<SettingsSchemaDoc> => {
      const { data } = await apiRequest('/api/v1/settings/schema', { op: 'settings schema fetch' });
      return SettingsSchemaDocSchema.parse(data);
    },
    staleTime: Infinity,
  });
}

export function useSettingsLayers() {
  return useQuery({
    queryKey: SETTINGS_LAYERS_KEY,
    queryFn: async (): Promise<SettingsLayersView> => {
      const { data } = await apiRequest('/api/v1/settings/layers', { op: 'settings fetch' });
      return SettingsLayersViewSchema.parse(data);
    },
  });
}

/** One save: the form sends `values` (null removes a key), the JSON view `raw`. */
export interface SaveSettingsInput {
  layer: SettingsLayerName;
  baseVersion: string;
  values?: Record<string, unknown>;
  raw?: string;
}

function saveMessage(res: SettingsSaveResult): string {
  const parts = [`Saved to the ${res.layer} settings`];
  if (res.restartRequired.length > 0)
    parts.push(`applies after a restart: ${res.restartRequired.join(', ')}`);
  const shadowed = Object.entries(res.shadowed);
  if (shadowed.length > 0)
    parts.push(
      `still overridden: ${shadowed.map(([key, layer]) => `${key} (by ${layer})`).join(', ')}`,
    );
  return parts.join('; ');
}

export function useSaveSettingsLayer() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (input: SaveSettingsInput): Promise<SettingsSaveResult> => {
      const body =
        input.raw !== undefined
          ? { baseVersion: input.baseVersion, raw: input.raw }
          : { baseVersion: input.baseVersion, values: input.values ?? {} };
      const { data } = await apiRequest(`/api/v1/settings/layers/${input.layer}`, {
        op: 'settings save',
        method: 'PUT',
        json: body,
      });
      return SettingsSaveResultSchema.parse(data);
    },
    onSuccess: (res) => {
      qc.setQueryData<SettingsLayersView>(SETTINGS_LAYERS_KEY, {
        schemaUrl: res.schemaUrl,
        layers: res.layers,
        effective: res.effective,
      });
      void useItervoxStore.getState().refreshSnapshot();
      useToastStore.getState().addToast(saveMessage(res), 'success');
    },
    onError: (err) => {
      if (err instanceof UnauthorizedError) return;
      if (err instanceof ApiError && err.status === 409) {
        void qc.invalidateQueries({ queryKey: SETTINGS_LAYERS_KEY });
        useToastStore
          .getState()
          .addToast(
            'The settings file changed since it was loaded. Reloaded it; apply your edit again.',
            'error',
          );
        return;
      }
      const message = err instanceof Error ? err.message : 'Unexpected error';
      useToastStore.getState().addToast(`Settings not saved: ${message}`, 'error');
    },
  });
}
