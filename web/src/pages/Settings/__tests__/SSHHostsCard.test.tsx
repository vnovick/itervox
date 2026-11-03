import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { SSHHostsCard } from '../SSHHostsCard';
import { useItervoxStore } from '../../../store/itervoxStore';
import { makeSnapshot } from '../../../test/fixtures/snapshots';

const setDispatchStrategy = vi.fn().mockResolvedValue(true);
vi.mock('../../../hooks/useSettingsActions', () => ({
  useSettingsActions: () => ({
    addSSHHost: vi.fn(),
    removeSSHHost: vi.fn(),
    setDispatchStrategy,
  }),
}));

function withStrategy(dispatchStrategy: string | undefined) {
  useItervoxStore.setState({
    snapshot: makeSnapshot({
      sshHosts: [{ host: 'build-1.example.com', description: '' }],
      dispatchStrategy,
    }),
  });
}

describe('SSHHostsCard dispatch strategy', () => {
  beforeEach(() => {
    setDispatchStrategy.mockClear();
  });

  it('marks Least Loaded when the daemon reports it', () => {
    withStrategy('least-loaded');
    render(<SSHHostsCard />);
    const group = screen.getByRole('radiogroup', { name: 'Dispatch strategy' });
    expect(group).toBeInTheDocument();
    expect(screen.getByRole('radio', { name: /Least Loaded/ })).toBeChecked();
    expect(screen.getByRole('radio', { name: /Round Robin/ })).not.toBeChecked();
  });

  it.each([undefined, '', 'fifo'])(
    'an unset or unrecognised value (%s) shows the round-robin default selected',
    (value) => {
      withStrategy(value);
      render(<SSHHostsCard />);
      expect(screen.getByRole('radio', { name: /Round Robin/ })).toBeChecked();
      expect(screen.getByRole('radio', { name: /Least Loaded/ })).not.toBeChecked();
    },
  );

  it('choosing an option saves it', () => {
    withStrategy('round-robin');
    render(<SSHHostsCard />);
    fireEvent.click(screen.getByRole('radio', { name: /Least Loaded/ }));
    expect(setDispatchStrategy).toHaveBeenCalledWith('least-loaded');
  });
});
