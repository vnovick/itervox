import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ComponentType, ReactNode } from 'react';
import IssueChangesTab from '../IssueChangesTab';
import { useUIStore } from '../../../../store/uiStore';
import { routeFetch } from './fetchMock';
import { codeFirstSplit, lanesResponse } from './lanesFixture';

vi.mock('../../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
import { authedFetch } from '../../../../auth/authedFetch';
const mockFetch = vi.mocked(authedFetch);

// React Flow needs a measured canvas; render each node through its custom
// node type and each edge as a labelled span instead.
vi.mock('@xyflow/react', () => ({
  ReactFlow: ({
    nodes,
    edges,
    nodeTypes,
    onNodeClick,
  }: {
    nodes: { id: string; type: string; data: unknown }[];
    edges: { id: string; label?: string }[];
    nodeTypes: Record<string, ComponentType<{ id: string; data: unknown }>>;
    onNodeClick?: (event: unknown, node: { id: string; data: unknown }) => void;
  }) => (
    <div data-testid="react-flow">
      {nodes.map((n) => {
        const Node = nodeTypes[n.type];
        return (
          // eslint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-static-element-interactions -- stands in for React Flow's node wrapper
          <div key={n.id} onClick={() => onNodeClick?.({}, n)}>
            <Node id={n.id} data={n.data} />
          </div>
        );
      })}
      {edges.map((e) => (
        <span key={e.id} data-testid={`edge-${e.id}`}>
          {e.label}
        </span>
      ))}
    </div>
  ),
  Background: () => null,
  Controls: () => null,
  Handle: () => null,
  Position: { Left: 'left', Right: 'right', Top: 'top', Bottom: 'bottom' },
}));

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

const diff = {
  identifier: 'ENG-1',
  base: 'main',
  mergeBase: '0123456789abcdef0123',
  hunksAllowed: true,
  sources: ['worktree'],
  source: 'worktree',
  files: [
    { path: '.github/workflows/ci.yml', status: 'modified', additions: 1, deletions: 0 },
    { path: 'main.go', status: 'modified', additions: 2, deletions: 1 },
  ],
};
const ciPatch =
  'diff --git a/.github/workflows/ci.yml b/.github/workflows/ci.yml\n--- a/.github/workflows/ci.yml\n+++ b/.github/workflows/ci.yml\n@@ -9,0 +10 @@\n+continue-on-error: true\n';

function routes(lanes: unknown) {
  routeFetch(mockFetch, {
    '/api/v1/issues/ENG-1/diff': { body: diff },
    '/api/v1/issues/ENG-1/diff?source=worktree': { body: diff },
    '/api/v1/issues/ENG-1/lanes': { body: lanes },
    '/api/v1/issues/ENG-1/diff?file=.github%2Fworkflows%2Fci.yml&source=worktree': {
      body: {
        identifier: 'ENG-1',
        base: 'main',
        file: diff.files[0],
        patch: ciPatch,
        bytes: ciPatch.length,
        maxBytes: 262144,
      },
    },
  });
}

async function openLanes() {
  render(<IssueChangesTab identifier="ENG-1" />, { wrapper });
  await userEvent.click(await screen.findByRole('button', { name: 'Lanes' }));
  return screen.findByTestId('lanes-diagram');
}

describe('Lanes view (guardrails.lanes)', () => {
  beforeEach(() => {
    mockFetch.mockReset();
    useUIStore.setState({ issueLanesFor: null });
  });

  it('shows files in lane columns with their rule and findings, and why a person is needed', async () => {
    routes(lanesResponse());
    const diagram = await openLanes();
    expect(within(diagram).getByText('Human')).toBeInTheDocument();
    expect(within(diagram).getByText('Second opinion')).toBeInTheDocument();
    const ci = within(diagram).getByTestId('lane-file-.github/workflows/ci.yml');
    expect(ci).toHaveTextContent('matched .github/**');
    const finding = within(ci).getByText(/continue-on-error: true/);
    expect(finding).toHaveTextContent(':10 continue-on-error: true');
    expect(finding.className).toContain('danger');
    const skip = within(within(diagram).getByTestId('lane-file-calc_test.go')).getByText(/t\.Skip/);
    expect(skip.className).toContain('warning');
    expect(within(diagram).getByTestId('lane-file-main.go')).toHaveTextContent('no lane rule');
    expect(within(diagram).getAllByTestId('lane-pr')).toHaveLength(1);

    const why = screen.getByRole('list', { name: 'Why this needs a person' });
    expect(within(why).getAllByRole('listitem')[0]).toHaveTextContent(
      '.github/workflows/ci.yml:10 adds continue-on-error: true',
    );
    expect(screen.getByText(/highest lane/)).toHaveTextContent('Human');
  });

  it('draws a split as two stacked PRs, says which goes first and why, and toggles to one PR', async () => {
    routes(lanesResponse({ split: codeFirstSplit as never }));
    const diagram = await openLanes();
    const prs = within(diagram).getAllByTestId('lane-pr');
    expect(prs.map((p) => p.textContent)).toEqual([
      expect.stringContaining('PR 1 · code · goes first'),
      expect.stringContaining('PR 2 · pipeline · stacked'),
    ]);
    expect(within(diagram).getByTestId('edge-pr-code->pr-pipeline')).toHaveTextContent(
      'stacked on',
    );
    expect(screen.getByTestId('lanes-split')).toHaveTextContent('The code goes first');
    expect(screen.getByTestId('lanes-split')).toHaveTextContent(
      'Why: the code passed its checks alone',
    );

    await userEvent.click(screen.getByRole('button', { name: 'One PR' }));
    expect(within(diagram).getAllByTestId('lane-pr')).toHaveLength(1);
    expect(screen.getByRole('button', { name: 'One PR' })).toHaveAttribute('aria-pressed', 'true');
  });

  it('clicking a file opens its diff in the Files view', async () => {
    routes(lanesResponse());
    const diagram = await openLanes();
    await userEvent.click(
      within(diagram).getByRole('button', { name: '.github/workflows/ci.yml' }),
    );
    const viewer = await screen.findByTestId('diff-viewer');
    expect(viewer).toHaveTextContent('+continue-on-error: true');
    expect(screen.getByRole('button', { name: 'Files' })).toHaveAttribute('aria-pressed', 'true');
  });

  it('opens on Lanes when an approval asked for it, once', async () => {
    routes(lanesResponse({ textAllowed: false }));
    useUIStore.setState({ issueLanesFor: 'ENG-1' });
    render(<IssueChangesTab identifier="ENG-1" />, { wrapper });
    expect(await screen.findByTestId('lanes-view')).toBeInTheDocument();
    expect(useUIStore.getState().issueLanesFor).toBeNull();
    expect(screen.getByText(/need an operator token/)).toBeInTheDocument();
  });

  it('says when there is no worktree', async () => {
    routeFetch(mockFetch, {
      '/api/v1/issues/ENG-1/diff': { body: diff },
      '/api/v1/issues/ENG-1/lanes': { status: 404, body: { error: { code: 'no_worktree' } } },
    });
    render(<IssueChangesTab identifier="ENG-1" />, { wrapper });
    await userEvent.click(await screen.findByRole('button', { name: 'Lanes' }));
    expect(await screen.findByText(/no lanes/)).toBeInTheDocument();
  });
});
