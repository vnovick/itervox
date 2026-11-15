import { useMutation, useQueries, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiRequest } from '../auth/apiRequest';
import { HUB_API_BASE, hubProjectApiUrl } from '../auth/apiBase';
import { UnauthorizedError } from '../auth/UnauthorizedError';
import { subscribeBrowserPush } from '../lib/pwa';
import { useToastStore } from '../store/toastStore';
import { inputRequiredRowState } from '../utils/inputRequired';
import {
  HubAttentionStateSchema,
  HubDevicesResponseSchema,
  HubPairingSchema,
  HubPushDevicesResponseSchema,
  type HubPairing,
  type HubProject,
} from '../types/schemas';

// #231 — the phone's server state: the cross-project needs-attention list
// (each project's snapshot slice through the hub proxy), push devices,
// paired devices and pairing codes. Hub paths are absolute (/hub/api/v1).

/** One row of the needs-attention list. */
export interface AttentionItem {
  project: string;
  identifier: string;
  kind: 'input' | 'failure';
  /** The question (input) or the redacted failure message. */
  text: string;
  at: string;
}

const ATTENTION_POLL_MS = 15_000;
const RUN_FAILURE_KINDS: readonly string[] = ['worker_failed', 'worker_stalled'];

/** Projects worth reading: reachable through the hub and with something waiting. */
export function projectsNeedingAttention(projects: readonly HubProject[]): HubProject[] {
  return projects.filter((p) => {
    const c = p.summary?.counts;
    return p.proxied && ((c?.inputRequired ?? 0) > 0 || (c?.recentFailures ?? 0) > 0);
  });
}

/** The needs-attention rows of every project, oldest question first. */
export function useHubAttention(projects: readonly HubProject[]) {
  const wanted = projectsNeedingAttention(projects);
  return useQueries({
    queries: wanted.map((p) => ({
      queryKey: ['hub', 'attention', p.name] as const,
      queryFn: async (): Promise<AttentionItem[]> => {
        const { data } = await apiRequest(hubProjectApiUrl(p.name, '/api/v1/state'), {
          op: `read ${p.name}`,
        });
        const state = HubAttentionStateSchema.parse(data);
        const inputs: AttentionItem[] = state.inputRequired
          .filter((r) => inputRequiredRowState(r) === 'input_required')
          .map((r) => ({
            project: p.name,
            identifier: r.identifier,
            kind: 'input',
            text: r.context,
            at: r.queuedAt,
          }));
        const failures: AttentionItem[] = state.recentFailures
          .filter((f) => RUN_FAILURE_KINDS.includes(f.kind) && f.identifier)
          .map((f) => ({
            project: p.name,
            identifier: f.identifier ?? '',
            kind: 'failure',
            text: f.message,
            at: f.occurredAt,
          }));
        return [...inputs, ...failures];
      },
      refetchInterval: ATTENTION_POLL_MS,
    })),
    combine: (results) => ({
      items: results
        .flatMap((r) => r.data ?? [])
        .sort((a, b) =>
          a.kind === b.kind ? a.at.localeCompare(b.at) : a.kind === 'input' ? -1 : 1,
        ),
      isLoading: results.some((r) => r.isLoading),
      failedProjects: results.filter((r) => r.isError).length,
    }),
  });
}

/** The mobile issue page of a project's issue (opens that project's app). */
export function mobileIssueHref(project: string, identifier: string): string {
  return `/hub/p/${encodeURIComponent(project)}/m/${encodeURIComponent(identifier)}`;
}

function toastError(err: unknown, what: string): void {
  if (err instanceof UnauthorizedError) return; // AuthGate handles the UI
  const message = err instanceof Error ? err.message : 'unknown error';
  useToastStore.getState().addToast(`${what}: ${message}`, 'error');
}

export const HUB_PUSH_DEVICES_KEY = ['hub', 'pushDevices'] as const;
export const HUB_DEVICES_KEY = ['hub', 'devices'] as const;

export function useHubPushDevices() {
  return useQuery({
    queryKey: HUB_PUSH_DEVICES_KEY,
    queryFn: async () => {
      const { data } = await apiRequest(`${HUB_API_BASE}/push/devices`, {
        op: 'list push devices',
      });
      return HubPushDevicesResponseSchema.parse(data).devices;
    },
  });
}

/** Subscribes this browser to the hub's pushes under a device name. */
export function useSubscribePush() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (name: string) => {
      const { data } = await apiRequest(`${HUB_API_BASE}/push/vapid-public-key`, {
        op: 'read the push key',
      });
      const key = (data as { publicKey?: unknown } | undefined)?.publicKey;
      if (typeof key !== 'string' || key === '') throw new Error('the hub has no push key');
      const subscription = await subscribeBrowserPush(key);
      await apiRequest(`${HUB_API_BASE}/push/subscribe`, {
        op: 'subscribe to notifications',
        method: 'POST',
        json: { name, subscription },
      });
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: HUB_PUSH_DEVICES_KEY });
      useToastStore.getState().addToast('Notifications are on for this device.', 'success');
    },
    onError: (err) => {
      toastError(err, 'Could not turn on notifications');
    },
  });
}

export function useRevokePushDevice() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      await apiRequest(`${HUB_API_BASE}/push/devices/${encodeURIComponent(id)}`, {
        op: 'remove a push device',
        method: 'DELETE',
      });
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: HUB_PUSH_DEVICES_KEY });
    },
    onError: (err) => {
      toastError(err, 'Could not remove the device');
    },
  });
}

export function useHubDevices() {
  return useQuery({
    queryKey: HUB_DEVICES_KEY,
    queryFn: async () => {
      const { data } = await apiRequest(`${HUB_API_BASE}/devices`, { op: 'list paired devices' });
      return HubDevicesResponseSchema.parse(data).devices;
    },
  });
}

export function useRevokeHubDevice() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      await apiRequest(`${HUB_API_BASE}/devices/${encodeURIComponent(id)}`, {
        op: 'revoke a device',
        method: 'DELETE',
      });
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: HUB_DEVICES_KEY });
      void qc.invalidateQueries({ queryKey: HUB_PUSH_DEVICES_KEY });
    },
    onError: (err) => {
      toastError(err, 'Could not revoke the device');
    },
  });
}

export function useCreatePairing() {
  return useMutation({
    mutationFn: async (): Promise<HubPairing> => {
      const { data } = await apiRequest(`${HUB_API_BASE}/pairing`, {
        op: 'create a pairing code',
        method: 'POST',
      });
      return HubPairingSchema.parse(data);
    },
    onError: (err) => {
      toastError(err, 'Could not create a pairing code');
    },
  });
}
