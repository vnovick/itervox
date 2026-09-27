// Skills inventory queries + scan mutation (T-89).
//
// `useSkillsInventory` and `useSkillsIssues` are read queries; `useSkillsScan`
// triggers a re-scan and invalidates both queries on success. The dashboard's
// Skills/Analytics tab uses these.

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ApiError, apiRequest } from '../auth/apiRequest';
import { UnauthorizedError } from '../auth/UnauthorizedError';
import { useToastStore } from '../store/toastStore';
import {
  AnalyticsSnapshotSchema,
  InventoryIssueSchema,
  InventorySchema,
  RecommendationSchema,
  type AnalyticsSnapshotData,
  type InventoryIssue,
  type Recommendation,
  type SkillsInventory,
} from '../types/schemas';
import { z } from 'zod';

export const SKILLS_INVENTORY_KEY = ['skills', 'inventory'] as const;
export const SKILLS_ISSUES_KEY = ['skills', 'issues'] as const;
export const SKILLS_ANALYTICS_KEY = ['skills', 'analytics'] as const;
export const SKILLS_ANALYTICS_RECS_KEY = ['skills', 'analytics', 'recs'] as const;

const STALE_TIME_MS = 5 * 60 * 1000; // 5 minutes — inventory rarely changes

function readErrorMessage(err: unknown): string {
  if (err instanceof Error) return err.message;
  return 'Unexpected error';
}

/** GET that maps a 503 (not computed yet) to null; any other failure throws ApiError. */
async function getOrNullOn503(path: string, op: string): Promise<unknown> {
  try {
    const { data } = await apiRequest(path, { op });
    return data;
  } catch (err) {
    if (err instanceof ApiError && err.status === 503) return null;
    throw err;
  }
}

export function useSkillsInventory() {
  return useQuery({
    queryKey: SKILLS_INVENTORY_KEY,
    queryFn: async (): Promise<SkillsInventory | null> => {
      // A 503 means the daemon hasn't completed its first scan yet — "no
      // data", not a hard error.
      const data = await getOrNullOn503('/api/v1/skills/inventory', 'inventory fetch');
      return data === null ? null : InventorySchema.parse(data);
    },
    staleTime: STALE_TIME_MS,
    retry: (failureCount, err) => {
      if (err instanceof UnauthorizedError) return false;
      return failureCount < 2;
    },
  });
}

const IssuesArraySchema = z.array(InventoryIssueSchema).nullable();

export function useSkillsIssues() {
  return useQuery({
    queryKey: SKILLS_ISSUES_KEY,
    queryFn: async (): Promise<InventoryIssue[]> => {
      const { data } = await apiRequest('/api/v1/skills/issues', { op: 'issues fetch' });
      const parsed = IssuesArraySchema.parse(data);
      return parsed ?? [];
    },
    staleTime: STALE_TIME_MS,
    retry: (failureCount, err) => {
      if (err instanceof UnauthorizedError) return false;
      return failureCount < 2;
    },
  });
}

export function useSkillsScan() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (): Promise<SkillsInventory> => {
      const { data } = await apiRequest('/api/v1/skills/scan', { op: 'scan', method: 'POST' });
      return InventorySchema.parse(data);
    },
    onSuccess: (inv) => {
      qc.setQueryData(SKILLS_INVENTORY_KEY, inv);
      void qc.invalidateQueries({ queryKey: SKILLS_ISSUES_KEY });
    },
    onError: (err) => {
      if (err instanceof UnauthorizedError) return;
      useToastStore.getState().addToast(`Skills scan failed: ${readErrorMessage(err)}`, 'error');
    },
  });
}

interface FixRequest {
  issueID: string;
  fix: {
    Label: string;
    Action: string;
    Target?: string;
    Destructive: boolean;
  };
}

const RecsArraySchema = z.array(RecommendationSchema).nullable();

export function useSkillsAnalytics() {
  return useQuery({
    queryKey: SKILLS_ANALYTICS_KEY,
    queryFn: async (): Promise<AnalyticsSnapshotData | null> => {
      const data = await getOrNullOn503('/api/v1/skills/analytics', 'analytics fetch');
      return data === null ? null : AnalyticsSnapshotSchema.parse(data);
    },
    staleTime: STALE_TIME_MS,
    retry: (failureCount, err) => {
      if (err instanceof UnauthorizedError) return false;
      return failureCount < 2;
    },
  });
}

export function useSkillsAnalyticsRecommendations() {
  return useQuery({
    queryKey: SKILLS_ANALYTICS_RECS_KEY,
    queryFn: async (): Promise<Recommendation[]> => {
      const { data } = await apiRequest('/api/v1/skills/analytics/recommendations', {
        op: 'analytics-recs fetch',
      });
      const parsed = RecsArraySchema.parse(data);
      return parsed ?? [];
    },
    staleTime: STALE_TIME_MS,
    retry: (failureCount, err) => {
      if (err instanceof UnauthorizedError) return false;
      return failureCount < 2;
    },
  });
}

export function useSkillsFix() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (req: FixRequest): Promise<void> => {
      await apiRequest('/api/v1/skills/fix', { op: 'fix', method: 'POST', json: req });
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: SKILLS_INVENTORY_KEY });
      void qc.invalidateQueries({ queryKey: SKILLS_ISSUES_KEY });
      useToastStore.getState().addToast('Fix applied — inventory re-scanning.', 'success');
    },
    onError: (err) => {
      if (err instanceof UnauthorizedError) return;
      useToastStore.getState().addToast(`Fix failed: ${readErrorMessage(err)}`, 'error');
    },
  });
}
