import { describe, it, expect } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { RunResources } from '../RunResources';
import { usage } from './fixtures';
import { fmtBytes, fmtCpu } from '../../../../utils/format';

describe('RunResources (system visibility)', () => {
  it('shows CPU, memory with its peak, processes, disk I/O and the top commands', () => {
    render(<RunResources usage={usage} />);
    const tiles = screen.getByRole('list', { name: 'Resource use' });
    expect(within(tiles).getByText('83%')).toBeInTheDocument();
    expect(within(tiles).getByText('700 MB')).toBeInTheDocument();
    expect(within(tiles).getByText('peak 870 MB')).toBeInTheDocument();
    expect(within(tiles).getByText('6')).toBeInTheDocument();
    expect(within(tiles).getByText('peak 9')).toBeInTheDocument();
    expect(within(tiles).getByText('3.0 MB')).toBeInTheDocument();
    expect(within(tiles).getByText('1.0 MB read')).toBeInTheDocument();
    expect(screen.getByText('2 open network connections')).toBeInTheDocument();
    const table = screen.getByRole('table', { name: 'Top child commands' });
    expect(within(table).getByText('node')).toBeInTheDocument();
    expect(within(table).getByText(/cli\.js --print/)).toBeInTheDocument();
    expect(within(table).getByText('0.4%')).toBeInTheDocument();
  });

  it('shows connections instead of disk I/O where the OS has none, and the SSH note', () => {
    render(
      <RunResources
        usage={{ ...usage, diskIoKnown: false, remote: true, topCommands: [], netConnections: 4 }}
      />,
    );
    expect(screen.getByText('Connections')).toBeInTheDocument();
    expect(screen.getByText('4')).toBeInTheDocument();
    expect(screen.queryByText('Disk written')).not.toBeInTheDocument();
    expect(screen.getByText(/only the local ssh client is measured/)).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });
});

describe('fmtBytes / fmtCpu', () => {
  it.each([
    [0, '0 B'],
    [512, '512 B'],
    [1536, '1.5 KB'],
    [5 * 1024 ** 3, '5.0 GB'],
    [150 * 1024 ** 2, '150 MB'],
    [-3, '0 B'],
  ])('fmtBytes(%d) = %s', (n, want) => {
    expect(fmtBytes(n)).toBe(want);
  });
  it.each([
    [0, '0%'],
    [0.25, '0.3%'],
    [182.4, '182%'],
  ])('fmtCpu(%d) = %s', (n, want) => {
    expect(fmtCpu(n)).toBe(want);
  });
});
