import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ServerDownScreen } from '../ServerDownScreen';
import { useAuthStore } from '../authStore';

beforeEach(() => {
  useAuthStore.setState({ status: 'serverDown', rejectedReason: null });
});

describe('ServerDownScreen', () => {
  it('renders the "cannot reach daemon" message', () => {
    render(<ServerDownScreen onRetry={() => undefined} />);
    expect(screen.getByText(/can't reach the daemon/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument();
  });

  // CORE-089 — the manual Retry only flips status to 'unknown'; AuthGate's
  // effect then probes exactly once. Calling onRetry too probed twice.
  it('manual Retry resets auth status to unknown without calling onRetry', async () => {
    const onRetry = vi.fn();
    const user = userEvent.setup();
    render(<ServerDownScreen onRetry={onRetry} />);

    await user.click(screen.getByRole('button', { name: /retry/i }));

    expect(onRetry).not.toHaveBeenCalled();
    expect(useAuthStore.getState().status).toBe('unknown');
  });

  describe('auto-retry (CORE-089)', () => {
    beforeEach(() => {
      vi.useFakeTimers();
    });
    afterEach(() => {
      vi.useRealTimers();
    });

    it('calls onRetry automatically after 5s', () => {
      const onRetry = vi.fn();
      render(<ServerDownScreen onRetry={onRetry} />);
      expect(screen.getByText(/retrying automatically every 5 seconds/i)).toBeInTheDocument();
      vi.advanceTimersByTime(4_999);
      expect(onRetry).not.toHaveBeenCalled();
      vi.advanceTimersByTime(1);
      expect(onRetry).toHaveBeenCalledTimes(1);
      vi.advanceTimersByTime(5_000);
      expect(onRetry).toHaveBeenCalledTimes(2);
    });

    it('clears the auto-retry timer on unmount', () => {
      const onRetry = vi.fn();
      const { unmount } = render(<ServerDownScreen onRetry={onRetry} />);
      unmount();
      vi.advanceTimersByTime(30_000);
      expect(onRetry).not.toHaveBeenCalled();
      expect(vi.getTimerCount()).toBe(0);
    });
  });
});
