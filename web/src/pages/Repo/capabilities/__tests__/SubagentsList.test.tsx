import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { SubagentsList } from '../SubagentsList';

describe('SubagentsList', () => {
  it('lists each subagent with its source, model, tools and size', () => {
    render(
      <SubagentsList
        subagents={[
          {
            Name: 'code-reviewer',
            Provider: 'claude',
            Source: 'project',
            Model: 'sonnet',
            Tools: ['Read', 'Grep'],
            ApproxTokens: 1500,
          },
          { Name: 'planner', Provider: 'claude', Source: 'user', ApproxTokens: 40 },
          {
            Name: 'reviewer',
            Provider: 'claude',
            Source: 'plugin:demo',
            Tools: null,
            ApproxTokens: 0,
          },
        ]}
      />,
    );
    expect(screen.getByText('@agent-code-reviewer')).toBeInTheDocument();
    expect(screen.getByText('project · sonnet · Read, Grep · 1.5k tok')).toBeInTheDocument();
    expect(screen.getByText('user · all tools · 40 tok')).toBeInTheDocument();
    expect(screen.getByText('plugin demo · all tools · 0 tok')).toBeInTheDocument();
  });

  it('explains where subagents come from when there are none', () => {
    render(<SubagentsList subagents={null} />);
    expect(screen.getByText(/No subagents found in \.claude\/agents/)).toBeInTheDocument();
  });
});
