import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { renderHook, waitFor } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { authedFetch } from '../../auth/authedFetch';
import {
  useSkillsAnalytics,
  useSkillsAnalyticsRecommendations,
  useSkillsFix,
  useSkillsInventory,
  useSkillsIssues,
  useSkillsScan,
} from '../skills';
import {
  InventoryIssueSchema,
  InventorySchema,
  SkillSchema,
  type SkillsInventory,
} from '../../types/schemas';

vi.mock('../../auth/authedFetch', () => ({
  authedFetch: vi.fn(),
}));

const mockAuthedFetch = vi.mocked(authedFetch);

const minimalInventory = {
  ScanTime: '2026-04-29T00:00:00Z',
  Skills: [
    {
      Name: 'demo',
      Description: 'demo skill',
      Provider: 'claude',
      Source: 'project',
      FilePath: '/p/.claude/skills/demo/SKILL.md',
      ApproxTokens: 50,
      TriggerPatterns: ['/demo'],
    },
  ],
  MCPServers: [{ Name: 'ctx7', Transport: 'stdio', Command: 'npx', Source: 'project-settings' }],
  Hooks: [],
  Instructions: [],
  Plugins: [],
  Issues: [],
};

function jsonResponse(body: unknown, init?: ResponseInit): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
    ...init,
  });
}

function createWrapper() {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false },
      mutations: { retry: false },
    },
  });
  const wrapper = ({ children }: { children: React.ReactNode }) =>
    React.createElement(QueryClientProvider, { client }, children);
  return {
    client,
    wrapper,
  };
}

beforeEach(() => {
  mockAuthedFetch.mockReset();
});

const sampleIssue = {
  ID: 'DUPLICATE_MCP',
  Severity: 'warn',
  Title: 'Duplicate MCP server registration',
  Description: 'Two registrations match.',
  Affected: ['ctx7@project-settings', 'ctx7-dup@user-settings'],
  Fix: { Label: 'Remove duplicates', Action: 'remove-mcp', Destructive: true },
};

describe('skills schemas', () => {
  it('parses a minimal inventory', () => {
    const inv: SkillsInventory = InventorySchema.parse(minimalInventory);
    expect(inv.Skills?.[0].Name).toBe('demo');
  });

  it('skills array tolerates null/undefined', () => {
    const inv = InventorySchema.parse({
      ScanTime: '2026-04-29T00:00:00Z',
      Skills: null,
    });
    expect(inv.Skills).toBeNull();
  });

  it('Inventory schema parses partial scan warning state', () => {
    const inv = InventorySchema.parse({
      ...minimalInventory,
      Partial: true,
      ScanError: 'codex scanner failed',
    });
    expect(inv.Partial).toBe(true);
    expect(inv.ScanError).toBe('codex scanner failed');
  });

  it('Inventory schema preserves stale scan status', () => {
    const inv = InventorySchema.parse({
      ...minimalInventory,
      Stale: true,
    });
    expect(inv.Stale).toBe(true);
  });

  it('Skill schema rejects missing required fields', () => {
    expect(() => SkillSchema.parse({ Description: 'no name' })).toThrow();
  });

  it('InventoryIssue schema parses with destructive Fix', () => {
    const issue = InventoryIssueSchema.parse(sampleIssue);
    expect(issue.Fix?.Destructive).toBe(true);
  });

  it('InventoryIssue schema parses without Fix', () => {
    const issue = InventoryIssueSchema.parse({
      ID: 'INFO_ONLY',
      Severity: 'info',
      Title: 'x',
      Description: 'y',
    });
    expect(issue.Fix).toBeUndefined();
  });
});

describe('skills queries', () => {
  it('fetches and parses the skills inventory through authedFetch', async () => {
    mockAuthedFetch.mockResolvedValueOnce(jsonResponse(minimalInventory));
    const { wrapper } = createWrapper();

    const { result } = renderHook(() => useSkillsInventory(), { wrapper });

    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });
    expect(mockAuthedFetch.mock.calls[0]).toEqual(['/api/v1/skills/inventory']);
    expect(result.current.data?.Skills?.[0].Name).toBe('demo');
  });

  it('treats inventory 503 as not-yet-scanned data', async () => {
    mockAuthedFetch.mockResolvedValueOnce(new Response('', { status: 503 }));
    const { wrapper } = createWrapper();

    const { result } = renderHook(() => useSkillsInventory(), { wrapper });

    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });
    expect(result.current.data).toBeNull();
  });

  it('normalizes a null issues response to an empty list', async () => {
    mockAuthedFetch.mockResolvedValueOnce(jsonResponse(null));
    const { wrapper } = createWrapper();

    const { result } = renderHook(() => useSkillsIssues(), { wrapper });

    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });
    expect(result.current.data).toEqual([]);
  });

  it('posts a scan request and seeds the inventory query cache', async () => {
    mockAuthedFetch.mockResolvedValueOnce(jsonResponse(minimalInventory));
    const { client, wrapper } = createWrapper();

    const { result } = renderHook(() => useSkillsScan(), { wrapper });
    result.current.mutate();

    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });
    expect(mockAuthedFetch.mock.calls[0]).toEqual(['/api/v1/skills/scan', { method: 'POST' }]);
    expect(client.getQueryData(['skills', 'inventory'])).toEqual(minimalInventory);
  });

  it('handles analytics availability and nullable recommendations', async () => {
    mockAuthedFetch
      .mockResolvedValueOnce(new Response('', { status: 503 }))
      .mockResolvedValueOnce(jsonResponse(null));
    const { wrapper } = createWrapper();

    const analytics = renderHook(() => useSkillsAnalytics(), { wrapper });
    await waitFor(() => {
      expect(analytics.result.current.isSuccess).toBe(true);
    });
    expect(analytics.result.current.data).toBeNull();

    const recs = renderHook(() => useSkillsAnalyticsRecommendations(), { wrapper });
    await waitFor(() => {
      expect(recs.result.current.isSuccess).toBe(true);
    });
    expect(recs.result.current.data).toEqual([]);
  });

  it('posts skill fixes and invalidates inventory queries', async () => {
    mockAuthedFetch.mockResolvedValueOnce(new Response(null, { status: 204 }));
    const { client, wrapper } = createWrapper();
    const invalidateSpy = vi.spyOn(client, 'invalidateQueries');

    const { result } = renderHook(() => useSkillsFix(), { wrapper });
    result.current.mutate({
      issueID: 'UNUSED_PROFILE',
      fix: {
        Label: 'Disable profile',
        Action: 'disable-profile',
        Target: 'old-profile',
        Destructive: false,
      },
    });

    await waitFor(() => {
      expect(result.current.isSuccess).toBe(true);
    });
    expect(mockAuthedFetch.mock.calls[0]).toEqual([
      '/api/v1/skills/fix',
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          issueID: 'UNUSED_PROFILE',
          fix: {
            Label: 'Disable profile',
            Action: 'disable-profile',
            Target: 'old-profile',
            Destructive: false,
          },
        }),
      },
    ]);
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ['skills', 'inventory'] });
    expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ['skills', 'issues'] });
  });
});
