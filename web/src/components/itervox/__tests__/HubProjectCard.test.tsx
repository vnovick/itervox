import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { HubProjectCard } from '../HubProjectCard';
import type { HubProject } from '../../../types/schemas';

function project(over: Partial<HubProject>): HubProject {
  return {
    name: 'web-app',
    workflow: '/code/web-app/WORKFLOW.md',
    projectDir: '/code/web-app',
    pid: 1,
    url: 'http://127.0.0.1:8090/',
    version: 'v0.3.1',
    startedAt: '',
    health: 'ok',
    proxied: true,
    capabilities: [],
    ...over,
  };
}

describe('HubProjectCard status', () => {
  it.each([
    [{ summary: { counts: { running: 2, inputRequired: 1 } } }, 'Needs you', 'dot-warn'],
    [{ summary: { counts: { running: 2 } } }, 'Healthy', 'dot-ok'],
    [{ summary: { counts: { running: 0 } } }, 'Idle', 'dot-idle'],
    [{ health: 'unauthorized' }, 'unauthorized', 'dot-warn'],
    [{ health: 'error' }, 'error', 'dot-danger'],
  ] as const)('%o shows %s', (over, label, dot) => {
    const { container } = render(<HubProjectCard project={project(over)} />);
    expect(screen.getByText(label)).toBeInTheDocument();
    expect(container.querySelector('.status-dot')).toHaveClass(dot);
  });

  it('says nothing is running when the member reports no work', () => {
    render(<HubProjectCard project={project({ summary: { counts: { running: 0 } } })} />);
    expect(screen.getByText('Nothing running')).toBeInTheDocument();
  });
});
