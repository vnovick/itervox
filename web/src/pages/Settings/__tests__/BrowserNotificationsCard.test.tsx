// CORE-096 — the opt-in control.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { BrowserNotificationsCard } from '../BrowserNotificationsCard';
import { useNotifyPrefsStore } from '../../../store/notifyPrefsStore';

const FakeNotification = {
  permission: 'default' as NotificationPermission,
  requestPermission: vi.fn(() => Promise.resolve<NotificationPermission>('granted')),
};

beforeEach(() => {
  FakeNotification.permission = 'default';
  FakeNotification.requestPermission.mockClear();
  vi.stubGlobal('Notification', FakeNotification);
  vi.stubGlobal('isSecureContext', true);
  useNotifyPrefsStore.setState({ optIn: false });
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe('BrowserNotificationsCard', () => {
  it('asks for permission on click and turns notifications on', async () => {
    const user = userEvent.setup();
    render(<BrowserNotificationsCard />);
    expect(FakeNotification.requestPermission).not.toHaveBeenCalled();
    FakeNotification.requestPermission.mockImplementationOnce(() => {
      FakeNotification.permission = 'granted';
      return Promise.resolve('granted');
    });
    await user.click(screen.getByRole('button', { name: 'Enable notifications' }));
    expect(FakeNotification.requestPermission).toHaveBeenCalledTimes(1);
    await waitFor(() => {
      expect(useNotifyPrefsStore.getState().optIn).toBe(true);
    });
    expect(await screen.findByRole('button', { name: 'Turn off' })).toBeInTheDocument();
  });

  it('explains the fallback and shows no button outside a secure context', () => {
    vi.stubGlobal('isSecureContext', false);
    render(<BrowserNotificationsCard />);
    expect(screen.getByText(/need HTTPS/)).toBeInTheDocument();
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });

  it('reports a refused prompt', async () => {
    const user = userEvent.setup();
    FakeNotification.requestPermission.mockImplementationOnce(() => Promise.resolve('default'));
    render(<BrowserNotificationsCard />);
    await user.click(screen.getByRole('button', { name: 'Enable notifications' }));
    expect(await screen.findByRole('status')).toHaveTextContent(/did not grant/);
    expect(useNotifyPrefsStore.getState().optIn).toBe(false);
  });
});
