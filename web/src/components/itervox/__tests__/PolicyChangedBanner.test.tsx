import { act, fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';
import { useItervoxStore } from '../../../store/itervoxStore';
import { makeSnapshot } from '../../../test/fixtures/snapshots';
import { PolicyChangedBanner } from '../PolicyChangedBanner';

const alert = {
  at: '2026-11-27T10:00:00Z',
  source: 'config_watcher',
  files: ['.itervox/settings.jsonc', 'WORKFLOW.md'],
  runIds: ['run-1'],
  possiblyAgent: true,
};

beforeEach(() => {
  window.localStorage.clear();
  useItervoxStore.setState({ snapshot: null });
});

describe('PolicyChangedBanner', () => {
  it('names the files, the runs and a possible agent change for an operator', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({ callerScope: 'operator', policyChanged: alert }),
    });
    render(<PolicyChangedBanner />);
    const banner = screen.getByTestId('policy-changed-banner');
    expect(banner).toHaveTextContent('.itervox/settings.jsonc, WORKFLOW.md');
    expect(banner).toHaveTextContent('active runs: run-1');
    expect(screen.getByTestId('policy-changed-by-agent')).toHaveTextContent(
      'Possibly changed by a running agent.',
    );
  });

  it('is hidden from a viewer and when there is no change', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({ callerScope: 'viewer', policyChanged: alert }),
    });
    const { unmount } = render(<PolicyChangedBanner />);
    expect(screen.queryByTestId('policy-changed-banner')).not.toBeInTheDocument();
    unmount();
    useItervoxStore.setState({ snapshot: makeSnapshot({ callerScope: 'admin' }) });
    render(<PolicyChangedBanner />);
    expect(screen.queryByTestId('policy-changed-banner')).not.toBeInTheDocument();
  });

  it('stays dismissed for that change and shows a newer one', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({ callerScope: 'admin', policyChanged: alert }),
    });
    const { unmount } = render(<PolicyChangedBanner />);
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }));
    expect(screen.queryByTestId('policy-changed-banner')).not.toBeInTheDocument();
    unmount();
    render(<PolicyChangedBanner />);
    expect(screen.queryByTestId('policy-changed-banner')).not.toBeInTheDocument();
    act(() => {
      useItervoxStore.setState({
        snapshot: makeSnapshot({
          callerScope: 'admin',
          policyChanged: { ...alert, at: '2026-11-27T11:00:00Z', possiblyAgent: false },
        }),
      });
    });
    expect(screen.getByTestId('policy-changed-banner')).toBeInTheDocument();
    expect(screen.queryByTestId('policy-changed-by-agent')).not.toBeInTheDocument();
  });
});
