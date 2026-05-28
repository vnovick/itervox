import { fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { PendingResumePanel } from '../PendingResumePanel';
import { useItervoxStore } from '../../../store/itervoxStore';
import { makePendingInputResumeRow, makeSnapshot } from '../../../test/fixtures/snapshots';

function renderPanel(onSelect = vi.fn()) {
  render(
    <MemoryRouter>
      <PendingResumePanel onSelect={onSelect} />
    </MemoryRouter>,
  );
  return onSelect;
}

describe('PendingResumePanel', () => {
  beforeEach(() => {
    useItervoxStore.setState({ snapshot: makeSnapshot() });
  });

  it('renders a local search control for pending resume content', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        inputRequired: [makePendingInputResumeRow({ identifier: 'ENG-RESUME' })],
      }),
    });

    renderPanel();

    expect(screen.getByRole('searchbox', { name: /search resuming queue/i })).toBeInTheDocument();
    expect(screen.getByText('ENG-RESUME')).toBeInTheDocument();
  });

  it('filters by identifier, profile, backend, and context without selecting rows', () => {
    const onSelect = vi.fn();
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        inputRequired: [
          makePendingInputResumeRow({
            identifier: 'ENG-PM',
            profile: 'pm',
            backend: 'claude',
            context: 'Ready for product review.',
          }),
          makePendingInputResumeRow({
            identifier: 'ENG-QA',
            profile: 'qa',
            backend: 'codex',
            context: 'Ready for browser verification.',
          }),
        ],
      }),
    });

    renderPanel(onSelect);

    fireEvent.change(screen.getByRole('searchbox', { name: /search resuming queue/i }), {
      target: { value: 'browser' },
    });

    expect(screen.getByText('ENG-QA')).toBeInTheDocument();
    expect(screen.queryByText('ENG-PM')).not.toBeInTheDocument();
    expect(onSelect).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole('button', { name: /clear queue search/i }));
    expect(screen.getByText('ENG-PM')).toBeInTheDocument();
  });

  it('uses distinct copy for an empty search result and still opens matching rows', () => {
    const onSelect = vi.fn();
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        inputRequired: [makePendingInputResumeRow({ identifier: 'ENG-RESUME' })],
      }),
    });

    renderPanel(onSelect);

    fireEvent.change(screen.getByRole('searchbox', { name: /search resuming queue/i }), {
      target: { value: 'missing' },
    });
    expect(screen.getByText('No matching resuming queue items')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: /clear queue search/i }));
    fireEvent.click(screen.getByRole('button', { name: /open pending resume ENG-RESUME/i }));

    expect(onSelect).toHaveBeenCalledWith('ENG-RESUME');
  });
});
