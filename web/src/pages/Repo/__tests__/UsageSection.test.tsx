// #200 — Repo Analysis → Runtime usage and cost: the runtime analytics with
// its recommendations, and the per-profile token breakdown.
import { render, screen, within } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { UsageSection } from '../usage/UsageSection';

const skillMocks = vi.hoisted(() => ({
  useSkillsAnalytics: vi.fn(),
  useSkillsAnalyticsRecommendations: vi.fn(),
}));
vi.mock('../../../queries/skills', () => skillMocks);

beforeEach(() => {
  skillMocks.useSkillsAnalytics.mockReturnValue({
    isLoading: false,
    data: {
      GeneratedAt: '2026-05-07T10:10:00Z',
      HasRuntimeEvidence: true,
      SkillStats: [],
      HookStats: [{ CapabilityID: 'hook:format', RuntimeLoads: 80 }],
      ProfileCosts: [
        {
          ProfileName: 'implementer',
          TotalApproxTokens: 12500,
          InstructionTokens: 4000,
          SkillTokens: 6000,
          HookTokens: 500,
          MCPToolSchemaTokens: 1600,
          WorkflowTemplateTokens: 400,
        },
      ],
    },
  });
  skillMocks.useSkillsAnalyticsRecommendations.mockReturnValue({
    data: [
      {
        ID: 'HOOK_STORM',
        Severity: 'warn',
        Title: 'Hook fires constantly',
        Description: 'format hook fired 80 times.',
        Affected: ['hook:format'],
      },
    ],
  });
});

describe('UsageSection (#200)', () => {
  it('renders the analytics recommendations and the per-profile token breakdown', () => {
    render(<UsageSection />);
    expect(screen.getByText('Hook fires constantly')).toBeInTheDocument();
    const table = screen.getByRole('table', { name: 'Tokens per profile' });
    const row = within(table).getByRole('row', { name: /implementer/ });
    expect(row).toHaveTextContent('4.0k');
    expect(row).toHaveTextContent('6.0k');
    expect(row).toHaveTextContent('13k');
  });

  it('explains how to get runtime evidence when there is none', () => {
    skillMocks.useSkillsAnalytics.mockReturnValue({ isLoading: false, data: null });
    skillMocks.useSkillsAnalyticsRecommendations.mockReturnValue({ data: [] });
    render(<UsageSection />);
    expect(screen.getByText(/runtime evidence not yet available/i)).toBeInTheDocument();
    expect(screen.getByText(/no per-profile breakdown yet/i)).toBeInTheDocument();
  });
});
