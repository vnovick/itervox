import { render, screen, fireEvent } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { SkillsCard } from '../SkillsCard';

const skillMocks = vi.hoisted(() => ({
  useSkillsInventory: vi.fn(),
  useSkillsIssues: vi.fn(),
  useSkillsScan: vi.fn(),
  useSkillsFix: vi.fn(),
  useSkillsAnalytics: vi.fn(),
  useSkillsAnalyticsRecommendations: vi.fn(),
}));

vi.mock('../../../queries/skills', () => skillMocks);

const scanMutate = vi.fn();
const fixMutate = vi.fn();

const inventory = {
  ScanTime: '2026-05-07T10:00:00Z',
  Skills: [
    {
      Name: 'graphify',
      Description: 'Build a knowledge graph.',
      Provider: 'codex',
      Source: 'user-codex',
      FilePath: '/skills/graphify/SKILL.md',
      ApproxTokens: 1200,
      TriggerPatterns: ['/graphify'],
    },
  ],
  Plugins: [
    {
      Name: 'github-tools',
      Provider: 'codex',
      Source: 'plugin:github',
      ApproxTokens: 800,
      Skills: [],
      Hooks: [],
    },
  ],
  MCPServers: [{ Name: 'github', Transport: 'stdio', Command: 'gh mcp', Source: 'project' }],
  Hooks: [
    {
      Event: 'PostToolUse',
      Matcher: 'Bash',
      Command: 'pnpm format',
      Provider: 'claude',
      Source: 'marketplace:hooks',
      ApproxTokens: 120,
    },
  ],
  Instructions: [
    {
      Name: 'CLAUDE.md',
      Provider: 'claude',
      Scope: 'project',
      FilePath: '/repo/CLAUDE.md',
      ApproxTokens: 500,
    },
  ],
  Issues: [],
};

beforeEach(() => {
  scanMutate.mockReset();
  fixMutate.mockReset();
  skillMocks.useSkillsInventory.mockReturnValue({ data: inventory, isLoading: false, error: null });
  skillMocks.useSkillsIssues.mockReturnValue({
    data: [
      {
        ID: 'UNUSED_PROFILE',
        Severity: 'warn',
        Title: 'Unused profile',
        Description: 'Profile has not run recently.',
        Affected: ['old-profile'],
        Fix: {
          Label: 'Disable profile',
          Action: 'edit-yaml',
          Target: 'agent.profiles.old-profile.enabled',
          Destructive: false,
        },
      },
    ],
  });
  skillMocks.useSkillsScan.mockReturnValue({ mutate: scanMutate, isPending: false });
  skillMocks.useSkillsFix.mockReturnValue({ mutate: fixMutate, isPending: false });
  skillMocks.useSkillsAnalytics.mockReturnValue({ data: null, isLoading: false });
  skillMocks.useSkillsAnalyticsRecommendations.mockReturnValue({ data: [] });
});

describe('SkillsCard', () => {
  it('renders loading and error states', () => {
    skillMocks.useSkillsInventory.mockReturnValue({ data: null, isLoading: true, error: null });
    const { rerender } = render(<SkillsCard />);
    expect(screen.getByText(/loading skills inventory/i)).toBeInTheDocument();

    skillMocks.useSkillsInventory.mockReturnValue({
      data: null,
      isLoading: false,
      error: new Error('offline'),
    });
    rerender(<SkillsCard />);
    expect(screen.getByText(/failed to load skills inventory/i)).toBeInTheDocument();
  });

  it('offers a first scan when inventory is unavailable', () => {
    skillMocks.useSkillsInventory.mockReturnValue({ data: null, isLoading: false, error: null });

    render(<SkillsCard />);

    fireEvent.click(screen.getByRole('button', { name: /run first scan/i }));
    expect(scanMutate).toHaveBeenCalledTimes(1);
  });

  it('renders inventory counts, expands skills, and applies non-destructive fixes', () => {
    render(<SkillsCard />);

    expect(screen.getByText('Inventory')).toBeInTheDocument();
    expect(screen.getByText('Recommendations')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /skills/i }));
    expect(screen.getByText('graphify')).toBeInTheDocument();
    expect(screen.getByText(/Codex \(user\)/)).toBeInTheDocument();

    fireEvent.click(screen.getByText('Unused profile'));
    expect(screen.getByText('How to optimize')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /disable profile/i }));
    expect(fixMutate).toHaveBeenCalledWith({
      issueID: 'UNUSED_PROFILE',
      fix: {
        Label: 'Disable profile',
        Action: 'edit-yaml',
        Target: 'agent.profiles.old-profile.enabled',
        Destructive: false,
      },
    });
  });

  it('expands every inventory section and renders duplicate MCP as advisory only', () => {
    skillMocks.useSkillsIssues.mockReturnValue({
      data: [
        {
          ID: 'DUPLICATE_MCP',
          Severity: 'warn',
          Title: 'Duplicate MCP',
          Description: 'Duplicate server registration.',
          Affected: ['github'],
        },
      ],
    });

    render(<SkillsCard />);

    fireEvent.click(screen.getByRole('button', { name: /plugins/i }));
    expect(screen.getByText('github-tools')).toBeInTheDocument();
    expect(screen.getByText(/Plugin \(github\)/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /mcp servers/i }));
    expect(screen.getByText('github')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /hooks/i }));
    expect(screen.getByText('PostToolUse')).toBeInTheDocument();
    expect(screen.getByText(/Marketplace \(hooks\)/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: /instructions/i }));
    expect(screen.getByText('/repo/CLAUDE.md')).toBeInTheDocument();

    fireEvent.click(screen.getByText('Duplicate MCP'));
    expect(screen.queryByRole('button', { name: /remove duplicates/i })).not.toBeInTheDocument();
  });

  it('guards destructive fixes with confirmation when backend marks a fix destructive', () => {
    skillMocks.useSkillsIssues.mockReturnValue({
      data: [
        {
          ID: 'CUSTOM_DESTRUCTIVE',
          Severity: 'warn',
          Title: 'Dangerous cleanup',
          Description: 'Synthetic destructive fix.',
          Affected: ['custom'],
          Fix: {
            Label: 'Delete file',
            Action: 'delete-file',
            Target: '/tmp/custom',
            Destructive: true,
          },
        },
      ],
    });
    const confirmSpy = vi
      .spyOn(window, 'confirm')
      .mockReturnValueOnce(false)
      .mockReturnValueOnce(true);

    render(<SkillsCard />);

    fireEvent.click(screen.getByText('Dangerous cleanup'));
    fireEvent.click(screen.getByRole('button', { name: /delete file/i }));
    expect(fixMutate).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: /delete file/i }));
    expect(confirmSpy).toHaveBeenCalledTimes(2);
    expect(fixMutate).toHaveBeenCalledWith({
      issueID: 'CUSTOM_DESTRUCTIVE',
      fix: {
        Label: 'Delete file',
        Action: 'delete-file',
        Target: '/tmp/custom',
        Destructive: true,
      },
    });
  });

  it('shows runtime analytics when log evidence exists', () => {
    skillMocks.useSkillsAnalytics.mockReturnValue({
      isLoading: false,
      data: {
        GeneratedAt: '2026-05-07T10:10:00Z',
        SkillStats: [{ CapabilityID: 'skill:graphify', RuntimeVerified: true }],
        HookStats: [{ CapabilityID: 'hook:format', RuntimeLoads: 2 }],
        ProfileCosts: [],
        Recommendations: [],
      },
    });
    skillMocks.useSkillsAnalyticsRecommendations.mockReturnValue({
      data: [
        {
          ID: 'BLOATED_PROFILE',
          Severity: 'warn',
          Title: 'Large profile',
          Description: 'Profile loads many tools.',
          Affected: ['default'],
        },
      ],
    });

    render(<SkillsCard />);

    expect(screen.getByText(/skills runtime-verified/i)).toBeInTheDocument();
    expect(screen.getByText('Large profile')).toBeInTheDocument();
  });
});
