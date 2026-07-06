// Skills inventory queries + scan mutation (T-89).
//
// `useSkillsInventory` and `useSkillsIssues` are read queries; `useSkillsScan`
// triggers a re-scan and invalidates both queries on success. The dashboard's
// Skills/Analytics tab uses these.

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { authedFetch } from '../auth/authedFetch';
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

export function useSkillsInventory() {
  return useQuery({
    queryKey: SKILLS_INVENTORY_KEY,
    queryFn: async (): Promise<SkillsInventory | null> => {
      const res = await authedFetch('/api/v1/skills/inventory');
      if (res.status === 503) {
        // Daemon hasn't completed first scan yet — treat as "no data" instead
        // of a hard error.
        return null;
      }
      if (!res.ok) throw new Error(`inventory fetch failed: ${String(res.status)}`);
      return InventorySchema.parse(await res.json());
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
      const res = await authedFetch('/api/v1/skills/issues');
      if (!res.ok) throw new Error(`issues fetch failed: ${String(res.status)}`);
      const parsed = IssuesArraySchema.parse(await res.json());
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
      const res = await authedFetch('/api/v1/skills/scan', { method: 'POST' });
      if (!res.ok) throw new Error(`scan failed: ${String(res.status)}`);
      return InventorySchema.parse(await res.json());
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
      const res = await authedFetch('/api/v1/skills/analytics');
      if (res.status === 503) return null;
      if (!res.ok) throw new Error(`analytics fetch failed: ${String(res.status)}`);
      return AnalyticsSnapshotSchema.parse(await res.json());
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
      const res = await authedFetch('/api/v1/skills/analytics/recommendations');
      if (!res.ok) throw new Error(`analytics-recs fetch failed: ${String(res.status)}`);
      const parsed = RecsArraySchema.parse(await res.json());
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
      const res = await authedFetch('/api/v1/skills/fix', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(req),
      });
      if (!res.ok) {
        const text = await res.text();
        throw new Error(text || `fix failed: ${String(res.status)}`);
      }
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
