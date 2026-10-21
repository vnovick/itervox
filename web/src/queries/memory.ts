// Memory page queries (#202).
//
// Memory lives in two files the daemon re-reads for every run, not in the
// SSE snapshot, so it is TanStack Query server state. Edits, pins and
// deletes update the cached list optimistically and roll back on error; a
// delete remembers the removed entry so the page's Undo can post it back.
// All requests go through apiRequest (authedFetch underneath).

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { apiRequest } from '../auth/apiRequest';
import { UnauthorizedError } from '../auth/UnauthorizedError';
import { useToastStore } from '../store/toastStore';
import { useUIStore } from '../store/uiStore';
import {
  MemoryEntrySchema,
  MemoryPromoteResultSchema,
  MemoryViewSchema,
  type MemoryEntry,
  type MemoryPromoteResult,
  type MemoryScope,
  type MemoryStatus,
  type MemoryType,
  type MemoryView,
} from '../types/schemas';

export const MEMORY_KEY = ['memory'] as const;

/** The entry as the create endpoint takes it back (the undo of a delete). */
function restorable(e: MemoryEntry): Omit<MemoryEntry, 'path' | 'runs'> {
  const entry: Partial<MemoryEntry> = { ...e };
  delete entry.path;
  delete entry.runs;
  return entry as Omit<MemoryEntry, 'path' | 'runs'>;
}

const MEMORY_STALE_MS = 10_000;

export function useMemory() {
  return useQuery({
    queryKey: MEMORY_KEY,
    queryFn: async (): Promise<MemoryView> => {
      const { data } = await apiRequest('/api/v1/memory', { op: 'memory fetch' });
      return MemoryViewSchema.parse(data);
    },
    staleTime: MEMORY_STALE_MS,
  });
}

/** A partial update (PUT /api/v1/memory/{id}); omitted fields stay. */
export interface MemoryPatch {
  type?: MemoryType;
  text?: string;
  profiles?: string[];
  tags?: string[];
  pinned?: boolean;
  status?: MemoryStatus;
}

export interface NewMemoryInput {
  text: string;
  type: MemoryType;
  scope: MemoryScope;
  tags?: string[];
  profiles?: string[];
}

function reportError(prefix: string, err: unknown) {
  if (err instanceof UnauthorizedError) return;
  const message = err instanceof Error ? err.message : 'Unexpected error';
  useToastStore.getState().addToast(`${prefix}: ${message}`, 'error');
}

function patchEntry(view: MemoryView | undefined, id: string, patch: Partial<MemoryEntry>) {
  if (!view) return view;
  return { ...view, entries: view.entries.map((e) => (e.id === id ? { ...e, ...patch } : e)) };
}

/** Optimistic list edit shared by update, pin and delete. */
function useOptimisticMemory() {
  const qc = useQueryClient();
  return {
    qc,
    async apply(change: (view: MemoryView | undefined) => MemoryView | undefined) {
      await qc.cancelQueries({ queryKey: MEMORY_KEY });
      const previous = qc.getQueryData<MemoryView>(MEMORY_KEY);
      qc.setQueryData<MemoryView | undefined>(MEMORY_KEY, change);
      return { previous };
    },
    rollback(ctx: { previous?: MemoryView } | undefined) {
      if (ctx?.previous) qc.setQueryData(MEMORY_KEY, ctx.previous);
    },
    settle() {
      void qc.invalidateQueries({ queryKey: MEMORY_KEY });
    },
  };
}

export function useCreateMemory() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (input: NewMemoryInput | MemoryEntry): Promise<MemoryEntry> => {
      const body = 'path' in input ? restorable(input) : input;
      const { data } = await apiRequest('/api/v1/memory', {
        op: 'memory save',
        method: 'POST',
        json: body,
      });
      return MemoryEntrySchema.parse(data);
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: MEMORY_KEY });
    },
    onError: (err) => {
      reportError('Memory not saved', err);
    },
  });
}

export function useUpdateMemory() {
  const opt = useOptimisticMemory();
  return useMutation({
    mutationFn: async ({ id, patch }: { id: string; patch: MemoryPatch }): Promise<MemoryEntry> => {
      const { data } = await apiRequest(`/api/v1/memory/${encodeURIComponent(id)}`, {
        op: 'memory update',
        method: 'PUT',
        json: patch,
      });
      return MemoryEntrySchema.parse(data);
    },
    onMutate: ({ id, patch }) => opt.apply((v) => patchEntry(v, id, patch)),
    onError: (err, _vars, ctx) => {
      opt.rollback(ctx);
      reportError('Memory not updated', err);
    },
    onSettled: () => {
      opt.settle();
    },
  });
}

export function usePinMemory() {
  const opt = useOptimisticMemory();
  return useMutation({
    mutationFn: async ({ id, pinned }: { id: string; pinned: boolean }): Promise<MemoryEntry> => {
      const { data } = await apiRequest(`/api/v1/memory/${encodeURIComponent(id)}/pin`, {
        op: 'memory pin',
        method: 'POST',
        json: { pinned },
      });
      return MemoryEntrySchema.parse(data);
    },
    onMutate: ({ id, pinned }) => opt.apply((v) => patchEntry(v, id, { pinned })),
    onError: (err, _vars, ctx) => {
      opt.rollback(ctx);
      reportError('Pin not saved', err);
    },
    onSettled: () => {
      opt.settle();
    },
  });
}

/** Delete with undo: the removed entry is kept in uiStore.memoryUndo. */
export function useDeleteMemory() {
  const opt = useOptimisticMemory();
  return useMutation({
    mutationFn: async (entry: MemoryEntry): Promise<MemoryEntry> => {
      const { data } = await apiRequest(`/api/v1/memory/${encodeURIComponent(entry.id)}`, {
        op: 'memory delete',
        method: 'DELETE',
      });
      return MemoryEntrySchema.parse(data);
    },
    onMutate: (entry) =>
      opt.apply((v) => (v ? { ...v, entries: v.entries.filter((e) => e.id !== entry.id) } : v)),
    onSuccess: (deleted) => {
      const ui = useUIStore.getState();
      ui.setMemoryUndo(deleted);
      if (ui.memorySelectedId === deleted.id) ui.setMemorySelectedId(null);
    },
    onError: (err, _entry, ctx) => {
      opt.rollback(ctx);
      reportError('Memory not deleted', err);
    },
    onSettled: () => {
      opt.settle();
    },
  });
}

export function usePromoteMemory() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (id: string): Promise<MemoryPromoteResult> => {
      const { data } = await apiRequest(`/api/v1/memory/${encodeURIComponent(id)}/promote`, {
        op: 'memory promote',
        method: 'POST',
      });
      return MemoryPromoteResultSchema.parse(data);
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: MEMORY_KEY });
      useToastStore.getState().addToast('Moved to the project memory', 'success');
    },
    onError: (err) => {
      reportError('Not promoted', err);
    },
  });
}

/** Approve a proposed lesson (#203): its scope, and its text when edited. */
export interface ApproveLessonInput {
  id: string;
  scope: MemoryScope;
  text?: string;
}

export function useApproveLesson() {
  const opt = useOptimisticMemory();
  return useMutation({
    mutationFn: async ({ id, scope, text }: ApproveLessonInput): Promise<MemoryEntry> => {
      const { data } = await apiRequest(`/api/v1/memory/${encodeURIComponent(id)}/approve`, {
        op: 'lesson approve',
        method: 'POST',
        json: text === undefined ? { scope } : { scope, text },
      });
      return MemoryEntrySchema.parse(data);
    },
    onMutate: ({ id, scope, text }) =>
      opt.apply((v) =>
        patchEntry(v, id, { status: 'active', scope, ...(text === undefined ? {} : { text }) }),
      ),
    onSuccess: () => {
      useToastStore.getState().addToast('Lesson approved: runs use it from now on', 'success');
    },
    onError: (err, _vars, ctx) => {
      opt.rollback(ctx);
      reportError('Lesson not approved', err);
    },
    onSettled: () => {
      opt.settle();
    },
  });
}

/** Reject a proposed lesson (#203); the daemon remembers it. */
export function useRejectLesson() {
  const opt = useOptimisticMemory();
  return useMutation({
    mutationFn: async (id: string): Promise<MemoryEntry> => {
      const { data } = await apiRequest(`/api/v1/memory/${encodeURIComponent(id)}/reject`, {
        op: 'lesson reject',
        method: 'POST',
      });
      return MemoryEntrySchema.parse(data);
    },
    onMutate: (id) =>
      opt.apply((v) => (v ? { ...v, entries: v.entries.filter((e) => e.id !== id) } : v)),
    onError: (err, _id, ctx) => {
      opt.rollback(ctx);
      reportError('Lesson not rejected', err);
    },
    onSettled: () => {
      opt.settle();
    },
  });
}
