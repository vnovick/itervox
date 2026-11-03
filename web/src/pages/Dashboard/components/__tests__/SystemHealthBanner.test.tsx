import { describe, it, expect, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { SystemHealthBanner } from '../SystemHealthBanner';
import { useItervoxStore } from '../../../../store/itervoxStore';
import { makeSnapshot } from '../../../../test/fixtures/snapshots';
import type { SystemStatus } from '../../../../types/schemas';

const GB = 1024 ** 3;

function system(machine: Partial<SystemStatus['machine']>): SystemStatus {
  return {
    platform: 'linux',
    activityMode: 'sampled',
    runs: { runs: 0, cpuPercent: 0, rssBytes: 0, processes: 0 },
    machine: {
      load1: 1,
      load5: 1,
      load15: 1,
      loadKnown: true,
      cpuCount: 8,
      memTotalBytes: 16 * GB,
      memAvailableBytes: 8 * GB,
      memPressure: 'ok',
      disk: { path: '/ws', freeBytes: 50 * GB, totalBytes: 500 * GB },
      diskWarnBytes: 5 * GB,
      diskPauseBytes: 2 * GB,
      diskLow: false,
      diskCritical: false,
      ...machine,
    },
  };
}

describe('SystemHealthBanner (system visibility)', () => {
  beforeEach(() => {
    useItervoxStore.setState({ snapshot: makeSnapshot({}) });
  });

  it('renders nothing without the monitor or while the machine is fine', () => {
    const { container, rerender } = render(<SystemHealthBanner />);
    expect(container).toBeEmptyDOMElement();
    useItervoxStore.setState({ snapshot: makeSnapshot({ system: system({}) }) });
    rerender(<SystemHealthBanner />);
    expect(container).toBeEmptyDOMElement();
  });

  it('warns below the warning threshold and alerts below the pause threshold', () => {
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        system: system({
          disk: { path: '/ws', freeBytes: 3 * GB, totalBytes: 500 * GB },
          diskLow: true,
        }),
      }),
    });
    const { rerender } = render(<SystemHealthBanner />);
    const warn = screen.getByTestId('system-health-disk');
    expect(warn).toHaveAttribute('role', 'status');
    expect(warn).toHaveTextContent(
      'Low disk space: 3.0 GB free of 500 GB on the workspace volume (/ws)',
    );
    expect(warn).toHaveTextContent('below the 5.0 GB warning threshold');

    useItervoxStore.setState({
      snapshot: makeSnapshot({
        system: system({
          disk: { path: '/ws', freeBytes: GB, totalBytes: 500 * GB },
          diskLow: true,
          diskCritical: true,
          memPressure: 'high',
        }),
      }),
    });
    rerender(<SystemHealthBanner />);
    expect(screen.getByTestId('system-health-disk')).toHaveAttribute('role', 'alert');
    expect(screen.getByTestId('system-health-disk')).toHaveTextContent('New runs are paused');
    expect(screen.getByTestId('system-health-memory')).toHaveTextContent('Memory pressure is high');
  });
});
