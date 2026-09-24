import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import BoardColumn from '../BoardColumn';
import type { TrackerIssue } from '../../../types/schemas';

vi.mock('@dnd-kit/core', () => ({
  useDraggable: () => ({
    attributes: {},
    listeners: {},
    setNodeRef: vi.fn(),
    transform: null,
    isDragging: false,
  }),
  useDroppable: () => ({ setNodeRef: vi.fn(), isOver: false }),
  DndContext: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  closestCenter: vi.fn(),
  // eslint-disable-next-line @typescript-eslint/no-extraneous-class
  PointerSensor: class {},
  useSensor: vi.fn(),
  useSensors: vi.fn(() => []),
}));

vi.mock('@dnd-kit/utilities', () => ({
  CSS: {
    Translate: { toString: vi.fn(() => '') },
  },
}));

// IssueCard renders the issue; mock it to keep tests focused on BoardColumn.
// `syncing` is surfaced via data-syncing so tests can assert the
// syncingIdentifiers join (BoardColumn -> DraggableCard -> IssueCard)
// reaches the leaf without re-testing IssueCard's own badge rendering
// (covered by IssueCard.test.tsx).
vi.mock('../IssueCard', () => ({
  default: ({
    issue,
    onSelect,
    syncing,
  }: {
    issue: TrackerIssue;
    onSelect: (id: string) => void;
    syncing?: boolean;
  }) => (
    <div
      data-testid="issue-card"
      data-syncing={syncing ? 'true' : 'false'}
      onClick={() => {
        onSelect(issue.identifier);
      }}
    >
      {issue.identifier}
    </div>
  ),
}));

const makeIssue = (id: string): TrackerIssue => ({
  identifier: id,
  title: `Title ${id}`,
  state: 'In Progress',
  description: '',
  url: '',
  orchestratorState: 'idle',
  turnCount: 0,
  tokens: 0,
  elapsedMs: 0,
  lastMessage: '',
  error: '',
});

describe('BoardColumn', () => {
  it('renders the column state label', () => {
    render(<BoardColumn state="In Progress" issues={[]} isOver={false} onSelect={vi.fn()} />);
    expect(screen.getByText('In Progress')).toBeInTheDocument();
  });

  it('shows the issue count badge', () => {
    const issues = [makeIssue('ABC-1'), makeIssue('ABC-2')];
    render(<BoardColumn state="Todo" issues={issues} isOver={false} onSelect={vi.fn()} />);
    expect(screen.getByText('2')).toBeInTheDocument();
  });

  it('shows 0 in count badge when no issues', () => {
    render(<BoardColumn state="Done" issues={[]} isOver={false} onSelect={vi.fn()} />);
    expect(screen.getByText('0')).toBeInTheDocument();
  });

  it('renders a card for each issue', () => {
    const issues = [makeIssue('XYZ-1'), makeIssue('XYZ-2'), makeIssue('XYZ-3')];
    render(<BoardColumn state="Backlog" issues={issues} isOver={false} onSelect={vi.fn()} />);
    expect(screen.getAllByTestId('issue-card')).toHaveLength(3);
  });

  it('shows overlay when isOver is true', () => {
    const { container } = render(
      <BoardColumn state="In Progress" issues={[]} isOver={true} onSelect={vi.fn()} />,
    );
    const overlay = container.querySelector('.pointer-events-none') as HTMLElement;
    expect(overlay).toBeInTheDocument();
    expect(overlay.className).toContain('opacity-100');
  });

  it('hides overlay when isOver is false', () => {
    const { container } = render(
      <BoardColumn state="In Progress" issues={[]} isOver={false} onSelect={vi.fn()} />,
    );
    const overlay = container.querySelector('.pointer-events-none') as HTMLElement;
    expect(overlay.className).toContain('opacity-0');
  });

  // All columns use bg-theme-bg-soft per the prototype lane spec
  it('applies bg-soft class for all states', () => {
    for (const state of ['Done', 'In Progress', 'Blocked', 'Backlog']) {
      const { container } = render(
        <BoardColumn state={state} issues={[]} isOver={false} onSelect={vi.fn()} />,
      );
      expect((container.firstChild as HTMLElement).className).toContain('bg-theme-bg-soft');
    }
  });

  it('calls onSelect with the issue identifier when a card is clicked', async () => {
    const onSelect = vi.fn();
    render(
      <BoardColumn
        state="In Progress"
        issues={[makeIssue('ABC-99')]}
        isOver={false}
        onSelect={onSelect}
      />,
    );
    await userEvent.click(screen.getByTestId('issue-card'));
    expect(onSelect).toHaveBeenCalledWith('ABC-99');
  });

  // outbox Task 4 — syncingIdentifiers is joined against each issue's
  // identifier (BoardColumn -> DraggableCard's `.has(issue.identifier)`)
  // and threaded down to IssueCard's `syncing` prop.
  describe('outbox Task 4: syncingIdentifiers join', () => {
    it('marks only issues present in syncingIdentifiers as syncing', () => {
      const issues = [makeIssue('ENG-1'), makeIssue('ENG-2')];
      render(
        <BoardColumn
          state="In Progress"
          issues={issues}
          isOver={false}
          onSelect={vi.fn()}
          syncingIdentifiers={new Set(['ENG-1'])}
        />,
      );
      const cards = screen.getAllByTestId('issue-card');
      const byIdentifier = new Map(cards.map((c) => [c.textContent, c]));
      expect(byIdentifier.get('ENG-1')).toHaveAttribute('data-syncing', 'true');
      expect(byIdentifier.get('ENG-2')).toHaveAttribute('data-syncing', 'false');
    });

    it('marks no issues as syncing when syncingIdentifiers is absent', () => {
      render(
        <BoardColumn
          state="In Progress"
          issues={[makeIssue('ENG-3')]}
          isOver={false}
          onSelect={vi.fn()}
        />,
      );
      expect(screen.getByTestId('issue-card')).toHaveAttribute('data-syncing', 'false');
    });
  });
});
