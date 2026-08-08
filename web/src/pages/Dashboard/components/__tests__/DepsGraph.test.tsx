import { fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { DepsGraph, layoutDependencyGraph } from '../DepsGraph';
import type {
  DependencyCycleRow,
  DependencyGraphEdge,
  DependencyGraphNode,
  DepsAnalyzeJob,
  ProfileDef,
} from '../../../../types/schemas';
import type { DepsJobUpdate } from '../../../../queries/deps';

// gaps_11 G-14 — mock the analyze mutation so picker tests can assert the
// payload without hitting the network. The disabled-reason logic under test
// lives entirely in the component, so the existing disabled-state tests are
// unaffected by this mock.
//
// Addendum (cancel UI) — `analyzeState.isPending` is a plain mutable object
// (not React state) that the mocked `useAnalyzeDeps` reads on every render.
// Tests that need to simulate "analysis is running" flip it — from inside
// `analyzeMutateSpy`'s mock implementation, mirroring how the real mutation
// flips `isPending` — before the click handler's synchronous work finishes,
// so the re-render triggered by React after the event handler observes the
// new value. This avoids reintroducing a second polling loop or fake timers
// into a component test that doesn't otherwise need them: DepsGraph itself
// does no polling — deps.ts's `pollUntilTerminal` does, and it is mocked out
// here entirely.
const {
  analyzeMutateSpy,
  cancelMutateSpy,
  overrideMutateSpy,
  analyzeState,
  cancelState,
  overrideState,
} = vi.hoisted(() => ({
  analyzeMutateSpy: vi.fn(),
  cancelMutateSpy: vi.fn(),
  overrideMutateSpy: vi.fn(),
  analyzeState: { isPending: false },
  cancelState: { isPending: false },
  overrideState: { isPending: false },
}));
vi.mock('../../../../queries/deps', () => ({
  useAnalyzeDeps: () => ({ mutate: analyzeMutateSpy, isPending: analyzeState.isPending }),
  useCancelAnalyzeDeps: () => ({ mutate: cancelMutateSpy, isPending: cancelState.isPending }),
  // unified-dependency-graph Task 8 — mocked identically to the sibling deps
  // mutations above so override-button tests can assert on the payload
  // without hitting authedFetch (that transport contract is covered by
  // web/src/queries/__tests__/deps.test.tsx instead).
  useSetDepsOverride: () => ({ mutate: overrideMutateSpy, isPending: overrideState.isPending }),
}));

function withQueryClient(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>;
}

vi.mock('@xyflow/react', () => ({
  ReactFlow: ({
    nodes,
    edges,
    onNodeClick,
  }: {
    nodes: Array<{ id: string; data: { label: React.ReactNode; identifier: string } }>;
    edges: Array<{ id: string; label?: string }>;
    onNodeClick?: (event: unknown, node: { data: { identifier: string } }) => void;
  }) => (
    <div data-testid="react-flow">
      {nodes.map((node) => (
        <button
          key={node.id}
          type="button"
          data-testid={`node-${node.id}`}
          onClick={() => onNodeClick?.({}, node)}
        >
          {node.data.label}
        </button>
      ))}
      {edges.map((edge) => (
        <span key={edge.id} data-testid={`edge-${edge.id}`}>
          {edge.label}
        </span>
      ))}
    </div>
  ),
  Background: () => <div data-testid="flow-background" />,
  Controls: () => <div data-testid="flow-controls" />,
}));

const graphNodes: DependencyGraphNode[] = [
  {
    id: 'DEMO-1',
    identifier: 'DEMO-1',
    title: 'Terminal blocker',
    state: 'Done',
    status: 'unblocked',
    running: false,
    queued: false,
    terminal: true,
  },
  {
    id: 'DEMO-2',
    identifier: 'DEMO-2',
    title: 'Blocked dependent',
    state: 'Backlog',
    status: 'blocked',
    running: true,
    queued: true,
    terminal: false,
  },
];

const graphEdges: DependencyGraphEdge[] = [
  {
    id: 'DEMO-1->DEMO-2',
    sourceIdentifier: 'DEMO-1',
    targetIdentifier: 'DEMO-2',
    sourceState: 'Done',
    targetState: 'Backlog',
    resolved: true,
    sourceKnown: true,
  },
];

describe('DepsGraph', () => {
  beforeEach(() => {
    analyzeMutateSpy.mockReset();
    cancelMutateSpy.mockReset();
    overrideMutateSpy.mockReset();
    analyzeState.isPending = false;
    cancelState.isPending = false;
    overrideState.isPending = false;
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('renders issue nodes, badges, and blocker edges', () => {
    render(
      withQueryClient(
        <DepsGraph graphNodes={graphNodes} graphEdges={graphEdges} onSelectIssue={vi.fn()} />,
      ),
    );

    expect(screen.getByText('DEMO-1')).toBeInTheDocument();
    expect(screen.getByText('DEMO-2')).toBeInTheDocument();
    expect(screen.getByText('Done')).toBeInTheDocument();
    expect(screen.getByText('running')).toBeInTheDocument();
    expect(screen.getByText('queued')).toBeInTheDocument();
    expect(screen.getByTestId('edge-DEMO-1->DEMO-2')).toHaveTextContent('resolved');
  });

  it('selects the existing issue detail path when a node is clicked', () => {
    const onSelectIssue = vi.fn();
    render(
      withQueryClient(
        <DepsGraph graphNodes={graphNodes} graphEdges={graphEdges} onSelectIssue={onSelectIssue} />,
      ),
    );

    fireEvent.click(screen.getByTestId('node-DEMO-2'));

    expect(onSelectIssue).toHaveBeenCalledWith('DEMO-2');
  });

  it('shows a compact empty state when there are no dependency edges', () => {
    render(withQueryClient(<DepsGraph graphNodes={[]} graphEdges={[]} onSelectIssue={vi.fn()} />));

    expect(screen.getByText('No dependency edges found.')).toBeInTheDocument();
  });

  it('uses a bounded mobile-safe canvas container', () => {
    render(
      withQueryClient(
        <DepsGraph graphNodes={graphNodes} graphEdges={graphEdges} onSelectIssue={vi.fn()} />,
      ),
    );

    const canvas = screen.getByTestId('deps-graph-canvas');
    expect(canvas.className).toContain('w-full');
    expect(canvas.className).toContain('overflow-hidden');
    expect(canvas.className).not.toContain('overflow-x-auto');
  });

  it('renders unknown blockers visibly instead of dropping the edge', () => {
    render(
      withQueryClient(
        <DepsGraph
          graphNodes={[
            {
              id: 'unknown:DEMO-4',
              identifier: 'unknown:DEMO-4',
              status: 'unknown',
              running: false,
              queued: false,
              terminal: false,
            },
            {
              id: 'DEMO-4',
              identifier: 'DEMO-4',
              status: 'unknown',
              running: false,
              queued: false,
              terminal: false,
            },
          ]}
          graphEdges={[
            {
              id: 'unknown:DEMO-4->DEMO-4',
              sourceIdentifier: 'unknown:DEMO-4',
              targetIdentifier: 'DEMO-4',
              resolved: false,
              sourceKnown: false,
            },
          ]}
          onSelectIssue={vi.fn()}
        />,
      ),
    );

    expect(screen.getByTestId('edge-unknown:DEMO-4->DEMO-4')).toHaveTextContent('unknown');
    expect(screen.getAllByText('unknown').length).toBeGreaterThan(0);
  });

  it('disables the Analyze button when no profile is configured', () => {
    render(
      withQueryClient(
        <DepsGraph graphNodes={graphNodes} graphEdges={graphEdges} onSelectIssue={vi.fn()} />,
      ),
    );
    const button = screen.getByRole('button', { name: /analyze dependencies/i });
    expect(button).toBeDisabled();
    expect(button.getAttribute('title')).toMatch(/deps_analyzer_profile/);
  });

  it('disables the Analyze button when the configured profile is unknown', () => {
    render(
      withQueryClient(
        <DepsGraph
          graphNodes={graphNodes}
          graphEdges={graphEdges}
          onSelectIssue={vi.fn()}
          depsAnalyzerProfile="missing-profile"
          profileDefs={{}}
        />,
      ),
    );
    const button = screen.getByRole('button', { name: /analyze dependencies/i });
    expect(button).toBeDisabled();
    expect(button.getAttribute('title')).toMatch(/not defined/);
  });

  it('disables the Analyze button when the configured profile is disabled', () => {
    const profileDef: ProfileDef = { command: 'claude', enabled: false };
    render(
      withQueryClient(
        <DepsGraph
          graphNodes={graphNodes}
          graphEdges={graphEdges}
          onSelectIssue={vi.fn()}
          depsAnalyzerProfile="deps-analyzer"
          profileDefs={{ 'deps-analyzer': profileDef }}
        />,
      ),
    );
    const button = screen.getByRole('button', { name: /analyze dependencies/i });
    expect(button).toBeDisabled();
    expect(button.getAttribute('title')).toMatch(/disabled/i);
  });

  it('enables the Analyze button when the profile is set, present, and enabled', () => {
    const profileDef: ProfileDef = { command: 'claude', enabled: true };
    render(
      withQueryClient(
        <DepsGraph
          graphNodes={graphNodes}
          graphEdges={graphEdges}
          onSelectIssue={vi.fn()}
          depsAnalyzerProfile="deps-analyzer"
          profileDefs={{ 'deps-analyzer': profileDef }}
        />,
      ),
    );
    const button = screen.getByRole('button', { name: /analyze dependencies/i });
    expect(button).not.toBeDisabled();
  });

  it('labels inferred edges with the inferred tag and dashed stroke', () => {
    const inferredEdge: DependencyGraphEdge = {
      id: 'A->B',
      sourceIdentifier: 'A',
      targetIdentifier: 'B',
      resolved: false,
      sourceKnown: true,
      origin: 'inferred',
      evidence: 'title mentions depends on A',
    };
    const result = layoutDependencyGraph(
      [
        { id: 'A', identifier: 'A', running: false, queued: false, terminal: false },
        { id: 'B', identifier: 'B', running: false, queued: false, terminal: false },
      ],
      [inferredEdge],
    );
    const edge = result.edges.find((e) => e.id === 'A->B');
    // Inferred edges surface a snippet of the
    // evidence string in the label. The full string remains on edge.data.
    expect(edge?.label).toMatch(/^inferred: /);
    expect(edge?.label).toContain('title mentions depends on A');
    expect((edge?.style as { strokeDasharray?: string } | undefined)?.strokeDasharray).toBe('4 2');
    // Full evidence string is still reachable via edge.data for future custom-
    // edge components.
    expect((edge?.data as { evidence?: string } | undefined)?.evidence).toBe(
      'title mentions depends on A',
    );
  });

  // unified-dependency-graph Task 8 — confidence/stale/overridden/gating
  // ride the edge as a bracketed badge suffix on the label (React Flow's
  // default edge type renders `label` as plain SVG text, so a fully styled
  // badge component would need a custom edge type — out of scope per the
  // brief's "don't redesign the graph" note) and are also exposed on
  // edge.data for a future custom-edge component to render richly.
  it('appends gating/stale/overridden badges to the inferred edge label', () => {
    const inferredEdge: DependencyGraphEdge = {
      id: 'A->B',
      sourceIdentifier: 'A',
      targetIdentifier: 'B',
      resolved: false,
      sourceKnown: true,
      origin: 'inferred',
      confidence: 0.42,
      stale: true,
      overridden: true,
      gating: true,
    };
    const result = layoutDependencyGraph(
      [
        { id: 'A', identifier: 'A', running: false, queued: false, terminal: false },
        { id: 'B', identifier: 'B', running: false, queued: false, terminal: false },
      ],
      [inferredEdge],
    );
    const edge = result.edges.find((e) => e.id === 'A->B');
    expect(edge?.label).toBe('inferred [gating, stale, overridden]');
    expect((edge?.style as { strokeDasharray?: string } | undefined)?.strokeDasharray).toBe('4 2');
    const data = edge?.data as
      | { stale?: boolean; overridden?: boolean; gating?: boolean }
      | undefined;
    expect(data?.stale).toBe(true);
    expect(data?.overridden).toBe(true);
    expect(data?.gating).toBe(true);
  });

  it('renders no badge suffix on an inferred edge with none of the flags set', () => {
    const inferredEdge: DependencyGraphEdge = {
      id: 'A->B',
      sourceIdentifier: 'A',
      targetIdentifier: 'B',
      resolved: false,
      sourceKnown: true,
      origin: 'inferred',
    };
    const result = layoutDependencyGraph(
      [
        { id: 'A', identifier: 'A', running: false, queued: false, terminal: false },
        { id: 'B', identifier: 'B', running: false, queued: false, terminal: false },
      ],
      [inferredEdge],
    );
    const edge = result.edges.find((e) => e.id === 'A->B');
    expect(edge?.label).toBe('inferred');
  });

  // Inferred edges with no evidence string fall back to
  // the bare 'inferred' label (the snippet is appended only when evidence
  // exists).
  it('falls back to bare "inferred" label when evidence is missing', () => {
    const inferredEdge: DependencyGraphEdge = {
      id: 'X->Y',
      sourceIdentifier: 'X',
      targetIdentifier: 'Y',
      resolved: false,
      sourceKnown: true,
      origin: 'inferred',
    };
    const result = layoutDependencyGraph(
      [
        { id: 'X', identifier: 'X', running: false, queued: false, terminal: false },
        { id: 'Y', identifier: 'Y', running: false, queued: false, terminal: false },
      ],
      [inferredEdge],
    );
    const edge = result.edges.find((e) => e.id === 'X->Y');
    expect(edge?.label).toBe('inferred');
    expect((edge?.data as { evidence?: string } | undefined)?.evidence).toBeUndefined();
  });

  // Long evidence strings are truncated so the visible
  // label stays readable on small graphs. The full string remains on
  // edge.data.evidence.
  it('truncates long evidence strings in the edge label', () => {
    const longEvidence = 'this is a very long evidence string that should be truncated for display';
    const inferredEdge: DependencyGraphEdge = {
      id: 'L->M',
      sourceIdentifier: 'L',
      targetIdentifier: 'M',
      resolved: false,
      sourceKnown: true,
      origin: 'inferred',
      evidence: longEvidence,
    };
    const result = layoutDependencyGraph(
      [
        { id: 'L', identifier: 'L', running: false, queued: false, terminal: false },
        { id: 'M', identifier: 'M', running: false, queued: false, terminal: false },
      ],
      [inferredEdge],
    );
    const edge = result.edges.find((e) => e.id === 'L->M');
    expect(edge?.label).toMatch(/…$/);
    // Full string still accessible via data.
    expect((edge?.data as { evidence?: string } | undefined)?.evidence).toBe(longEvidence);
  });

  // The in-canvas legend explains the dashed-vs-solid
  // convention so operators don't have to infer it from edge styling alone.
  it('renders the legend explaining tracker vs inferred edge styles', () => {
    render(
      withQueryClient(
        <DepsGraph graphNodes={graphNodes} graphEdges={graphEdges} onSelectIssue={vi.fn()} />,
      ),
    );
    expect(screen.getByText('tracker')).toBeInTheDocument();
    expect(screen.getByText('inferred')).toBeInTheDocument();
  });

  it('shows the last-analyzed label when a timestamp is present', () => {
    render(
      withQueryClient(
        <DepsGraph
          graphNodes={graphNodes}
          graphEdges={graphEdges}
          onSelectIssue={vi.fn()}
          depsLastAnalyzedAt={new Date(Date.now() - 60_000).toISOString()}
        />,
      ),
    );
    expect(screen.getByText(/Last analyzed/i)).toBeInTheDocument();
    // 60s ago should surface as "1m ago" (or similar relative time).
    expect(screen.getByText(/ago|just now/i)).toBeInTheDocument();
  });

  // gaps_11 G-14 / deps design §3.3 — profile picker dropdown in the toolbar.
  describe('profile picker', () => {
    const pickerProfileDefs: Record<string, ProfileDef> = {
      'deps-analyzer': { command: 'claude', enabled: true },
      'other-profile': { command: 'codex', enabled: true },
    };

    beforeEach(() => {
      analyzeMutateSpy.mockClear();
    });

    function renderWithPicker(profileDefs: Record<string, ProfileDef> = pickerProfileDefs) {
      render(
        withQueryClient(
          <DepsGraph
            graphNodes={graphNodes}
            graphEdges={graphEdges}
            onSelectIssue={vi.fn()}
            depsAnalyzerProfile="deps-analyzer"
            profileDefs={profileDefs}
          />,
        ),
      );
    }

    it('defaults the labelled picker to the configured analyzer profile and lists all profiles', () => {
      renderWithPicker();
      const select = screen.getByLabelText(/profile/i);
      expect(select).toHaveValue('deps-analyzer');
      const options = Array.from(select.querySelectorAll('option')).map((o) => o.value);
      expect(options).toEqual(['deps-analyzer', 'other-profile']);
    });

    it('sends the configured profile to the mutation when the picker is untouched', () => {
      renderWithPicker();
      fireEvent.click(screen.getByRole('button', { name: /analyze dependencies/i }));
      // Addendum (cancel UI) — the call now also carries `onJobUpdate`, the
      // callback deps.ts's poll loop uses to surface the job ID/progress
      // back to the component. Exact function identity isn't meaningful
      // here, so match it loosely.
      expect(analyzeMutateSpy).toHaveBeenCalledWith({
        profile: 'deps-analyzer',
        onJobUpdate: expect.any(Function),
      });
    });

    it('sends the picked profile to the mutation after changing the selection', () => {
      renderWithPicker();
      fireEvent.change(screen.getByLabelText(/profile/i), {
        target: { value: 'other-profile' },
      });
      fireEvent.click(screen.getByRole('button', { name: /analyze dependencies/i }));
      expect(analyzeMutateSpy).toHaveBeenCalledWith({
        profile: 'other-profile',
        onJobUpdate: expect.any(Function),
      });
    });

    it('re-evaluates the disabled state against the picked profile', () => {
      renderWithPicker({
        'deps-analyzer': { command: 'claude', enabled: false },
        'other-profile': { command: 'codex', enabled: true },
      });
      const button = screen.getByRole('button', { name: /analyze dependencies/i });
      // Configured profile is disabled → button disabled with the existing reason.
      expect(button).toBeDisabled();
      expect(button.getAttribute('title')).toMatch(/disabled/i);
      // Picking an enabled profile lifts the restriction.
      fireEvent.change(screen.getByLabelText(/profile/i), {
        target: { value: 'other-profile' },
      });
      expect(button).not.toBeDisabled();
    });

    it('hides the picker when no analyzer profile is configured', () => {
      render(
        withQueryClient(
          <DepsGraph graphNodes={graphNodes} graphEdges={graphEdges} onSelectIssue={vi.fn()} />,
        ),
      );
      expect(screen.queryByLabelText(/profile/i)).toBeNull();
    });
  });

  // Addendum (cancel UI) — an operator can stop a running analysis and
  // immediately start another. The mutation's job ID and chunk progress are
  // surfaced to the component via `onJobUpdate`, which the mocked
  // `useAnalyzeDeps.mutate` invokes synchronously here to simulate the
  // enqueue response / a poll tick landing, without any real polling loop or
  // fake timers — DepsGraph itself does not poll; deps.ts does, and it is
  // mocked out entirely in this file.
  describe('cancel UI', () => {
    const runnableProfileDefs: Record<string, ProfileDef> = {
      'deps-analyzer': { command: 'claude', enabled: true },
    };

    function renderRunnable() {
      render(
        withQueryClient(
          <DepsGraph
            graphNodes={graphNodes}
            graphEdges={graphEdges}
            onSelectIssue={vi.fn()}
            depsAnalyzerProfile="deps-analyzer"
            profileDefs={runnableProfileDefs}
          />,
        ),
      );
    }

    function mockRunningJob(update: DepsJobUpdate) {
      analyzeMutateSpy.mockImplementation(
        (input: { profile?: string; onJobUpdate?: (u: DepsJobUpdate) => void }) => {
          analyzeState.isPending = true;
          input.onJobUpdate?.(update);
        },
      );
    }

    it('has no Cancel control while idle', () => {
      renderRunnable();
      expect(screen.queryByRole('button', { name: /cancel dependency analysis/i })).toBeNull();
    });

    it('shows a Cancel control once a job is running', () => {
      mockRunningJob({ jobId: 'job-1', status: 'running' });
      renderRunnable();

      fireEvent.click(screen.getByRole('button', { name: /analyze dependencies/i }));

      expect(
        screen.getByRole('button', { name: /cancel dependency analysis/i }),
      ).toBeInTheDocument();
    });

    it('sends the DELETE mutation with the running job ID when Cancel is clicked', () => {
      mockRunningJob({ jobId: 'job-42', status: 'running' });
      renderRunnable();
      fireEvent.click(screen.getByRole('button', { name: /analyze dependencies/i }));

      fireEvent.click(screen.getByRole('button', { name: /cancel dependency analysis/i }));

      expect(cancelMutateSpy).toHaveBeenCalledWith({ jobId: 'job-42' });
    });

    it('renders chunk progress once chunksTotal is known', () => {
      mockRunningJob({ jobId: 'job-1', status: 'running', chunksTotal: 3, chunksDone: 1 });
      renderRunnable();

      fireEvent.click(screen.getByRole('button', { name: /analyze dependencies/i }));

      expect(screen.getByTestId('deps-analyze-progress')).toHaveTextContent('chunk 1 / 3');
    });

    it('suppresses the progress counter when chunksTotal is absent (older daemon / omitempty)', () => {
      mockRunningJob({ jobId: 'job-1', status: 'running' });
      renderRunnable();

      fireEvent.click(screen.getByRole('button', { name: /analyze dependencies/i }));

      expect(screen.queryByTestId('deps-analyze-progress')).toBeNull();
    });

    // chunksTotal is `omitempty` on the wire, so the server can never
    // actually emit `chunksTotal: 0` — the reachable "single-chunk run"
    // wire state is chunksTotal: 1 (also the default at any backlog <= the
    // default deps_analyzer_chunk_size of 75). This is the fixture a real
    // daemon could actually produce; the previous version of this test used
    // chunksTotal: 0, which is unreachable and didn't exercise the real
    // single-chunk suppression path.
    it('suppresses the progress counter when chunksTotal is 1 (single-chunk run, the reachable wire state)', () => {
      mockRunningJob({ jobId: 'job-1', status: 'running', chunksTotal: 1, chunksDone: 1 });
      renderRunnable();

      fireEvent.click(screen.getByRole('button', { name: /analyze dependencies/i }));

      expect(screen.queryByTestId('deps-analyze-progress')).toBeNull();
    });

    // On a single-chunk run the chunk counter above stays suppressed for
    // the run's whole duration, so lastActivityAt is the only liveness
    // signal available — it must render on its own, not only alongside the
    // chunk counter.
    it('renders last-activity liveness even when the chunk counter is suppressed', () => {
      mockRunningJob({
        jobId: 'job-1',
        status: 'running',
        chunksTotal: 1,
        chunksDone: 1,
        lastActivityAt: new Date().toISOString(),
      });
      renderRunnable();

      fireEvent.click(screen.getByRole('button', { name: /analyze dependencies/i }));

      const progress = screen.getByTestId('deps-analyze-progress');
      expect(progress).toHaveTextContent('last activity');
      expect(progress).not.toHaveTextContent('chunk');
    });

    it('renders both chunk progress and last-activity liveness when chunksTotal > 1', () => {
      mockRunningJob({
        jobId: 'job-1',
        status: 'running',
        chunksTotal: 3,
        chunksDone: 1,
        lastActivityAt: new Date().toISOString(),
      });
      renderRunnable();

      fireEvent.click(screen.getByRole('button', { name: /analyze dependencies/i }));

      const progress = screen.getByTestId('deps-analyze-progress');
      expect(progress).toHaveTextContent('chunk 1 / 3');
      expect(progress).toHaveTextContent('last activity');
    });
  });

  // analyzer-autonomy Task 5 — the running-job status area shows a small
  // "auto" badge when the in-flight job was scheduler-initiated (trigger:
  // 'auto'), so operators can distinguish a scheduled incremental pass from
  // one they started themselves by clicking "Analyze dependencies".
  describe('auto-trigger badge', () => {
    const runnableProfileDefs: Record<string, ProfileDef> = {
      'deps-analyzer': { command: 'claude', enabled: true },
    };

    function renderRunnable() {
      render(
        withQueryClient(
          <DepsGraph
            graphNodes={graphNodes}
            graphEdges={graphEdges}
            onSelectIssue={vi.fn()}
            depsAnalyzerProfile="deps-analyzer"
            profileDefs={runnableProfileDefs}
          />,
        ),
      );
    }

    function mockRunningJob(update: DepsJobUpdate) {
      analyzeMutateSpy.mockImplementation(
        (input: { profile?: string; onJobUpdate?: (u: DepsJobUpdate) => void }) => {
          analyzeState.isPending = true;
          input.onJobUpdate?.(update);
        },
      );
    }

    it('shows the "auto" badge when the running job was scheduler-initiated', () => {
      mockRunningJob({ jobId: 'job-1', status: 'running', trigger: 'auto' });
      renderRunnable();

      fireEvent.click(screen.getByRole('button', { name: /analyze dependencies/i }));

      expect(screen.getByTestId('deps-analyze-trigger-auto')).toHaveTextContent('auto');
    });

    it('hides the "auto" badge when the running job is manually-triggered', () => {
      mockRunningJob({ jobId: 'job-1', status: 'running', trigger: 'manual' });
      renderRunnable();

      fireEvent.click(screen.getByRole('button', { name: /analyze dependencies/i }));

      expect(screen.queryByTestId('deps-analyze-trigger-auto')).toBeNull();
    });

    it('hides the "auto" badge when the running job has no trigger field (older daemon)', () => {
      mockRunningJob({ jobId: 'job-1', status: 'running' });
      renderRunnable();

      fireEvent.click(screen.getByRole('button', { name: /analyze dependencies/i }));

      expect(screen.queryByTestId('deps-analyze-trigger-auto')).toBeNull();
    });

    it('hides the "auto" badge while idle, even before any job has run', () => {
      renderRunnable();
      expect(screen.queryByTestId('deps-analyze-trigger-auto')).toBeNull();
    });
  });

  // #46-1 — the Cancel control (and, per #52's passive-badge deferral, the
  // "auto" badge) must derive from server state (the snapshot's
  // depsAnalyzeJob row) so a page refresh/navigation mid-run still shows
  // them. Before this, both were gated on `analyzeDeps.isPending` /
  // `liveJob`, which are mutation-local React state that resets to
  // idle/null on every mount — exactly what a page refresh does. These
  // tests render with NO mutation state set (analyzeState.isPending stays
  // false, mockRunningJob/onJobUpdate is never invoked) to prove the
  // surfaces come from the `depsAnalyzeJob` prop alone.
  describe('snapshot-derived current job (#46-1 / #52 passive badge)', () => {
    const runnableProfileDefs: Record<string, ProfileDef> = {
      'deps-analyzer': { command: 'claude', enabled: true },
    };

    function runningSnapshotJob(overrides: Partial<DepsAnalyzeJob> = {}): DepsAnalyzeJob {
      return {
        jobId: 'job-refresh-1',
        status: 'running',
        queuedAt: '2026-05-28T12:00:00Z',
        ...overrides,
      };
    }

    function renderWithSnapshotJob(depsAnalyzeJob?: DepsAnalyzeJob) {
      render(
        withQueryClient(
          <DepsGraph
            graphNodes={graphNodes}
            graphEdges={graphEdges}
            onSelectIssue={vi.fn()}
            depsAnalyzerProfile="deps-analyzer"
            profileDefs={runnableProfileDefs}
            depsAnalyzeJob={depsAnalyzeJob}
          />,
        ),
      );
    }

    it('shows the Cancel control on first render from the snapshot row alone, with no click and no mutation state', () => {
      renderWithSnapshotJob(runningSnapshotJob());

      expect(
        screen.getByRole('button', { name: /cancel dependency analysis/i }),
      ).toBeInTheDocument();
      // The "Analyze dependencies" button itself must also reflect the
      // running state (disabled, "Analyzing…") since the daemon already
      // has this profile's only concurrent job slot occupied.
      expect(screen.getByRole('button', { name: /analyze dependencies/i })).toBeDisabled();
    });

    it('shows the Cancel control immediately when the DepsGraph is re-mounted mid-run (page refresh simulation)', () => {
      // Simulate a page refresh: a fresh mount with the snapshot already
      // carrying a running job, before React Query / the poll loop have
      // had any chance to run.
      const { unmount } = render(
        withQueryClient(
          <DepsGraph graphNodes={graphNodes} graphEdges={graphEdges} onSelectIssue={vi.fn()} />,
        ),
      );
      unmount();

      renderWithSnapshotJob(runningSnapshotJob());

      expect(
        screen.getByRole('button', { name: /cancel dependency analysis/i }),
      ).toBeInTheDocument();
    });

    it('sends the snapshot row jobId to the cancel mutation when Cancel is clicked with no prior click-driven state', () => {
      renderWithSnapshotJob(runningSnapshotJob({ jobId: 'job-refresh-42' }));

      fireEvent.click(screen.getByRole('button', { name: /cancel dependency analysis/i }));

      expect(cancelMutateSpy).toHaveBeenCalledWith({ jobId: 'job-refresh-42' });
    });

    it('shows the "auto" badge passively from the snapshot row alone, with no click', () => {
      renderWithSnapshotJob(runningSnapshotJob({ trigger: 'auto' }));

      expect(screen.getByTestId('deps-analyze-trigger-auto')).toHaveTextContent('auto');
    });

    it('hides the "auto" badge when the snapshot row is manually-triggered', () => {
      renderWithSnapshotJob(runningSnapshotJob({ trigger: 'manual' }));

      expect(screen.queryByTestId('deps-analyze-trigger-auto')).toBeNull();
    });

    it('shows chunk progress from the snapshot row alone', () => {
      renderWithSnapshotJob(runningSnapshotJob({ chunksTotal: 3, chunksDone: 2 }));

      expect(screen.getByTestId('deps-analyze-progress')).toHaveTextContent('chunk 2 / 3');
    });

    it('has no Cancel control and no auto badge when the snapshot job is terminal (last-run info, not in-flight)', () => {
      renderWithSnapshotJob(
        runningSnapshotJob({ status: 'succeeded', trigger: 'auto', jobId: 'job-done' }),
      );

      expect(screen.queryByRole('button', { name: /cancel dependency analysis/i })).toBeNull();
      expect(screen.queryByTestId('deps-analyze-trigger-auto')).toBeNull();
      expect(screen.getByRole('button', { name: /analyze dependencies/i })).not.toBeDisabled();
    });

    it('has no Cancel control when no analyzer job has ever run (depsAnalyzeJob absent)', () => {
      renderWithSnapshotJob(undefined);

      expect(screen.queryByRole('button', { name: /cancel dependency analysis/i })).toBeNull();
    });

    // The click-driven mutation/poll path (existing "cancel UI" / "auto-
    // trigger badge" describe blocks above) must keep working unmodified
    // when it is the only source of truth (snapshot job absent, covered by
    // those blocks already passing unmodified) AND take priority once a
    // click has actually started a job — the poll loop's `onJobUpdate`
    // ticks carry strictly fresher progress than a since-arrived snapshot
    // row for the same run (e.g. a slightly stale SSE tick).
    it('prefers the live click-driven job over a since-arrived snapshot row for the same run (fresher progress wins)', () => {
      // No job running yet — the button is enabled and clickable, matching
      // a real "operator starts a run" flow.
      const { rerender } = render(
        withQueryClient(
          <DepsGraph
            graphNodes={graphNodes}
            graphEdges={graphEdges}
            onSelectIssue={vi.fn()}
            depsAnalyzerProfile="deps-analyzer"
            profileDefs={runnableProfileDefs}
          />,
        ),
      );
      analyzeMutateSpy.mockImplementation(
        (input: { profile?: string; onJobUpdate?: (u: DepsJobUpdate) => void }) => {
          analyzeState.isPending = true;
          input.onJobUpdate?.({
            jobId: 'job-fresh',
            status: 'running',
            chunksTotal: 3,
            chunksDone: 3,
          });
        },
      );
      fireEvent.click(screen.getByRole('button', { name: /analyze dependencies/i }));
      expect(screen.getByTestId('deps-analyze-progress')).toHaveTextContent('chunk 3 / 3');

      // A snapshot tick for the SAME job now lands, but its progress is one
      // chunk behind the poll loop's latest tick (a real, benign race — SSE
      // and the poll loop are independent). The rendered progress must not
      // regress to the stale value.
      rerender(
        withQueryClient(
          <DepsGraph
            graphNodes={graphNodes}
            graphEdges={graphEdges}
            onSelectIssue={vi.fn()}
            depsAnalyzerProfile="deps-analyzer"
            profileDefs={runnableProfileDefs}
            depsAnalyzeJob={runningSnapshotJob({
              jobId: 'job-fresh',
              chunksTotal: 3,
              chunksDone: 2,
            })}
          />,
        ),
      );

      expect(screen.getByTestId('deps-analyze-progress')).toHaveTextContent('chunk 3 / 3');
      fireEvent.click(screen.getByRole('button', { name: /cancel dependency analysis/i }));
      expect(cancelMutateSpy).toHaveBeenCalledWith({ jobId: 'job-fresh' });
    });
  });

  it('lays out blockers left of blocked issues', () => {
    const layout = layoutDependencyGraph(graphNodes, graphEdges);
    const blocker = layout.nodes.find((node) => node.id === 'DEMO-1');
    const blocked = layout.nodes.find((node) => node.id === 'DEMO-2');

    expect(blocker?.position.x).toBeLessThan(blocked?.position.x ?? 0);
  });

  // unified-dependency-graph Task 8 — dismiss/restore control for a
  // selected issue's inferred blockers. `useSetDepsOverride` is mocked at
  // the top of this file identically to useAnalyzeDeps/useCancelAnalyzeDeps,
  // so these tests assert on the mutation payload rather than the network
  // transport (that contract lives in web/src/queries/__tests__/deps.test.tsx).
  describe('inferred-dependency override control', () => {
    const inferredGatingEdge: DependencyGraphEdge = {
      id: 'DEMO-1->DEMO-2-inferred',
      sourceIdentifier: 'DEMO-1',
      targetIdentifier: 'DEMO-2',
      resolved: false,
      sourceKnown: true,
      origin: 'inferred',
      evidence: 'DEMO-2 mentions blocked by DEMO-1',
      confidence: 0.9,
      gating: true,
    };

    const overriddenInferredEdge: DependencyGraphEdge = {
      ...inferredGatingEdge,
      gating: false,
      overridden: true,
    };

    it('renders no override panel before any node is selected', () => {
      render(
        withQueryClient(
          <DepsGraph
            graphNodes={graphNodes}
            graphEdges={[inferredGatingEdge]}
            onSelectIssue={vi.fn()}
          />,
        ),
      );
      expect(screen.queryByTestId('deps-override-panel')).toBeNull();
    });

    it('renders no override panel when the selected issue has no inferred edges', () => {
      render(
        withQueryClient(
          <DepsGraph graphNodes={graphNodes} graphEdges={graphEdges} onSelectIssue={vi.fn()} />,
        ),
      );
      fireEvent.click(screen.getByTestId('node-DEMO-2'));
      expect(screen.queryByTestId('deps-override-panel')).toBeNull();
    });

    it('shows "Dismiss inferred blockers" for a gating inferred edge on the selected issue and fires the mutation', () => {
      const onSelectIssue = vi.fn();
      render(
        withQueryClient(
          <DepsGraph
            graphNodes={graphNodes}
            graphEdges={[inferredGatingEdge]}
            onSelectIssue={onSelectIssue}
          />,
        ),
      );

      fireEvent.click(screen.getByTestId('node-DEMO-2'));
      // Selecting the node for the override panel does not disturb the
      // existing issue-detail navigation behaviour.
      expect(onSelectIssue).toHaveBeenCalledWith('DEMO-2');

      const panel = screen.getByTestId('deps-override-panel');
      expect(panel).toHaveTextContent('DEMO-2');
      const button = screen.getByRole('button', { name: 'Dismiss inferred blockers' });
      fireEvent.click(button);

      expect(overrideMutateSpy).toHaveBeenCalledWith({ identifier: 'DEMO-2', enabled: true });
    });

    it('shows "Restore inferred blockers" once the edge is overridden and fires the mutation with enabled: false', () => {
      render(
        withQueryClient(
          <DepsGraph
            graphNodes={graphNodes}
            graphEdges={[overriddenInferredEdge]}
            onSelectIssue={vi.fn()}
          />,
        ),
      );

      fireEvent.click(screen.getByTestId('node-DEMO-2'));

      const button = screen.getByRole('button', { name: 'Restore inferred blockers' });
      fireEvent.click(button);

      expect(overrideMutateSpy).toHaveBeenCalledWith({ identifier: 'DEMO-2', enabled: false });
    });

    it('selecting the blocker side (source, not target) of the inferred edge shows no panel', () => {
      render(
        withQueryClient(
          <DepsGraph
            graphNodes={graphNodes}
            graphEdges={[inferredGatingEdge]}
            onSelectIssue={vi.fn()}
          />,
        ),
      );
      fireEvent.click(screen.getByTestId('node-DEMO-1'));
      expect(screen.queryByTestId('deps-override-panel')).toBeNull();
    });
  });

  // critical-path-ordering Task 6 — cycle-member edges (source AND target
  // both in the same dependency cycle) get a distinct danger stroke that
  // overrides the normal resolved/blocking palette AND the dashed-inferred
  // style, plus tooltip data naming the cycle's members.
  describe('cycle-member edge highlight', () => {
    const cycleEdge: DependencyGraphEdge = {
      id: 'A->B',
      sourceIdentifier: 'A',
      targetIdentifier: 'B',
      resolved: false,
      sourceKnown: true,
    };
    const nonCycleEdge: DependencyGraphEdge = {
      id: 'B->C',
      sourceIdentifier: 'B',
      targetIdentifier: 'C',
      resolved: false,
      sourceKnown: true,
    };
    const trackerCycle: DependencyCycleRow = {
      members: ['A', 'B'],
      kind: 'tracker',
      detectedAt: '2026-05-25T12:00:00Z',
    };

    it('gives a cycle-member edge a danger stroke, no dash, and drops the normal blocking color', () => {
      const result = layoutDependencyGraph(
        [
          { id: 'A', identifier: 'A', running: false, queued: false, terminal: false },
          { id: 'B', identifier: 'B', running: false, queued: false, terminal: false },
        ],
        [cycleEdge],
        [trackerCycle],
      );
      const edge = result.edges.find((e) => e.id === 'A->B');
      const style = edge?.style as { stroke?: string; strokeDasharray?: string } | undefined;
      expect(style?.stroke).toBe('var(--danger, #ef4444)');
      expect(style?.strokeDasharray).toBeUndefined();
      expect(edge?.label).toContain('cycle');
    });

    it('does not highlight an edge whose target is outside the cycle', () => {
      const result = layoutDependencyGraph(
        [
          { id: 'A', identifier: 'A', running: false, queued: false, terminal: false },
          { id: 'B', identifier: 'B', running: false, queued: false, terminal: false },
          { id: 'C', identifier: 'C', running: false, queued: false, terminal: false },
        ],
        [cycleEdge, nonCycleEdge],
        [trackerCycle],
      );
      const edge = result.edges.find((e) => e.id === 'B->C');
      const style = edge?.style as { stroke?: string } | undefined;
      expect(style?.stroke).not.toBe('var(--danger, #ef4444)');
      expect(edge?.label).not.toContain('cycle');
    });

    it('overrides the dashed-inferred stroke pattern on a cycle-member inferred edge', () => {
      const inferredCycleEdge: DependencyGraphEdge = {
        ...cycleEdge,
        origin: 'inferred',
      };
      const result = layoutDependencyGraph(
        [
          { id: 'A', identifier: 'A', running: false, queued: false, terminal: false },
          { id: 'B', identifier: 'B', running: false, queued: false, terminal: false },
        ],
        [inferredCycleEdge],
        [trackerCycle],
      );
      const edge = result.edges.find((e) => e.id === 'A->B');
      const style = edge?.style as { strokeDasharray?: string } | undefined;
      expect(style?.strokeDasharray).toBeUndefined();
    });

    // critical-path-ordering Task 6 review fix — the per-edge red stroke +
    // `[cycle]` badge tells an operator THAT an edge is cyclic but names no
    // members and carries no remediation guidance (React Flow's default edge
    // type has no hover-tooltip surface without a custom edge component).
    // `DepsCycleBanner` is the actual visible/testable surface for that
    // information, rendered inside `DepsGraph` above the canvas.
    describe('DepsCycleBanner', () => {
      const baseNodes = [
        { id: 'A', identifier: 'A', running: false, queued: false, terminal: false },
        { id: 'B', identifier: 'B', running: false, queued: false, terminal: false },
      ];

      it('renders no banner when there are no cycles', () => {
        render(
          withQueryClient(
            <DepsGraph graphNodes={baseNodes} graphEdges={[cycleEdge]} onSelectIssue={vi.fn()} />,
          ),
        );
        expect(screen.queryByTestId('deps-cycle-banner')).toBeNull();
      });

      it('lists a tracker cycle by its members, with no artifact-caveat text', () => {
        render(
          withQueryClient(
            <DepsGraph
              graphNodes={baseNodes}
              graphEdges={[cycleEdge]}
              cycles={[trackerCycle]}
              onSelectIssue={vi.fn()}
            />,
          ),
        );
        const banner = screen.getByTestId('deps-cycle-banner');
        expect(banner).toHaveTextContent('A');
        expect(banner).toHaveTextContent('B');
        expect(banner).toHaveTextContent('tracker cycle');
        expect(banner.textContent).not.toMatch(/analyzer artifact/i);
      });

      it('shows the analyzer-artifact caveat and remediation guidance for an inferred cycle', () => {
        const inferredCycle: DependencyCycleRow = {
          members: ['A', 'B'],
          kind: 'inferred',
          detectedAt: '2026-05-25T12:00:00Z',
        };
        render(
          withQueryClient(
            <DepsGraph
              graphNodes={baseNodes}
              graphEdges={[cycleEdge]}
              cycles={[inferredCycle]}
              onSelectIssue={vi.fn()}
            />,
          ),
        );
        const banner = screen.getByTestId('deps-cycle-banner');
        expect(banner).toHaveTextContent('A');
        expect(banner).toHaveTextContent('B');
        expect(banner).toHaveTextContent('inferred cycle');
        expect(banner.textContent).toMatch(/analyzer artifact/i);
        expect(banner.textContent).toMatch(/override|re-run/i);
      });
    });

    it('renders no highlight when no cycles are passed (older-daemon / absent-field default)', () => {
      const result = layoutDependencyGraph(
        [
          { id: 'A', identifier: 'A', running: false, queued: false, terminal: false },
          { id: 'B', identifier: 'B', running: false, queued: false, terminal: false },
        ],
        [cycleEdge],
      );
      const edge = result.edges.find((e) => e.id === 'A->B');
      const style = edge?.style as { stroke?: string } | undefined;
      expect(style?.stroke).not.toBe('var(--danger, #ef4444)');
      expect(edge?.label).not.toContain('cycle');
    });

    it('threads the cycles prop through the DepsGraph component into the rendered edge label', () => {
      render(
        withQueryClient(
          <DepsGraph
            graphNodes={[
              {
                id: 'A',
                identifier: 'A',
                running: false,
                queued: false,
                terminal: false,
              },
              {
                id: 'B',
                identifier: 'B',
                running: false,
                queued: false,
                terminal: false,
              },
            ]}
            graphEdges={[cycleEdge]}
            cycles={[trackerCycle]}
            onSelectIssue={vi.fn()}
          />,
        ),
      );
      expect(screen.getByTestId('edge-A->B')).toHaveTextContent('cycle');
    });
  });
});
