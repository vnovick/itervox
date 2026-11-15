import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { HubPairingPanel } from '../HubPairingPanel';
import { PairDeviceScreen } from '../../../../auth/PairDeviceScreen';
import { AuthGate } from '../../../../auth/AuthGate';
import { useAuthStore } from '../../../../auth/authStore';
import { useTokenStore } from '../../../../auth/tokenStore';
import { configureApiContext } from '../../../../auth/apiBase';

function withQuery(ui: React.ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

const json = (body: unknown, status = 200) =>
  Promise.resolve(new Response(JSON.stringify(body), { status }));

beforeEach(() => {
  configureApiContext('/hub/');
});
afterEach(() => {
  configureApiContext('/');
  useTokenStore.getState().clearToken();
  window.location.hash = '';
  vi.restoreAllMocks();
});

// #231 — pairing a phone: the hub page's QR code and device list, and the
// phone's exchange of the one-time code for its own token.
describe('HubPairingPanel', () => {
  it('shows a one-time QR code and revokes a paired device', async () => {
    const calls: { url: string; method: string }[] = [];
    global.fetch = vi.fn((url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET';
      calls.push({ url, method });
      if (url === '/hub/api/v1/pairing') {
        return json(
          { code: 'ABCDEF', expiresAt: '2026-10-22T12:10:00Z', path: '/hub/pair#code=ABCDEF' },
          201,
        );
      }
      if (url === '/hub/api/v1/devices' && method === 'GET') {
        return json({
          devices: [
            { id: 'tok_1', name: 'Phone', created: '2026-10-22T12:00:00Z', pushDevices: 1 },
          ],
        });
      }
      if (method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }));
      return json({ devices: [] });
    }) as unknown as typeof fetch;
    withQuery(<HubPairingPanel />);

    fireEvent.click(screen.getByRole('button', { name: 'Show pairing QR code' }));
    expect(await screen.findByRole('img', { name: 'Pairing QR code' })).toBeTruthy();
    expect(screen.getByTestId('pairing-loopback')).toBeTruthy(); // jsdom runs on localhost
    fireEvent.change(screen.getByRole('textbox', { name: 'Hub address on the phone' }), {
      target: { value: 'https://mac.tailnet.ts.net/' },
    });
    const link = 'https://mac.tailnet.ts.net/hub/pair#code=ABCDEF';
    expect(screen.getByRole('link', { name: link }).getAttribute('href')).toBe(link);
    expect(screen.queryByTestId('pairing-loopback')).toBeNull();
    fireEvent.change(screen.getByRole('textbox', { name: 'Hub address on the phone' }), {
      target: { value: 'https://mac.tailnet.ts.net/hub' },
    });
    expect(screen.queryByRole('img', { name: 'Pairing QR code' })).toBeNull();
    expect(screen.getByRole('alert').textContent).toContain('no path');

    const devices = await screen.findByRole('list', { name: 'Paired devices' });
    expect(within(devices).getByText('notifications on')).toBeTruthy();
    fireEvent.click(within(devices).getByRole('button', { name: 'Revoke Phone' }));
    await waitFor(() => {
      expect(calls).toContainEqual({ url: '/hub/api/v1/devices/tok_1', method: 'DELETE' });
    });
  });
});

describe('PairDeviceScreen', () => {
  it('exchanges the code from the fragment for a remembered device token', async () => {
    window.location.hash = '#code=ABCDEF';
    let sent: unknown;
    global.fetch = vi.fn((_url: string, init?: RequestInit) => {
      sent = JSON.parse(typeof init?.body === 'string' ? init.body : 'null');
      return json(
        { token: 'device-token', device: { id: 'tok_1', name: 'Pixel', created: '' } },
        201,
      );
    }) as unknown as typeof fetch;
    const onPaired = vi.fn();
    render(<PairDeviceScreen onPaired={onPaired} />);
    fireEvent.change(screen.getByRole('textbox', { name: 'Device name' }), {
      target: { value: 'Pixel' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Pair' }));
    await waitFor(() => {
      expect(onPaired).toHaveBeenCalled();
    });
    expect(sent).toEqual({ code: 'ABCDEF', name: 'Pixel' });
    expect(useTokenStore.getState().token).toBe('device-token');
    expect(localStorage.getItem('itervox.apiToken.persistent')).toBe('device-token');
  });

  it('explains a used or expired code', async () => {
    window.location.hash = '#code=OLD';
    global.fetch = vi.fn(() =>
      json(
        { error: { code: 'invalid_code', message: 'the pairing code is wrong, used or expired' } },
        400,
      ),
    ) as unknown as typeof fetch;
    render(<PairDeviceScreen onPaired={vi.fn()} />);
    fireEvent.click(screen.getByRole('button', { name: 'Pair' }));
    expect((await screen.findByRole('alert')).textContent).toContain('used or expired');
    expect(useTokenStore.getState().token).toBeNull();
  });

  it('needs a code in the link', () => {
    render(<PairDeviceScreen onPaired={vi.fn()} />);
    expect(screen.getByRole('alert').textContent).toContain('no pairing code');
  });
});

describe('AuthGate on /hub/pair', () => {
  it('shows the pairing screen before any token, without probing', () => {
    configureApiContext('/hub/pair');
    useAuthStore.setState({ status: 'unknown', rejectedReason: null });
    const fetchMock = vi.fn();
    global.fetch = fetchMock as unknown as typeof fetch;
    render(
      <AuthGate>
        <p>app</p>
      </AuthGate>,
    );
    expect(screen.getByRole('heading', { name: 'Pair this device with the hub' })).toBeTruthy();
    expect(screen.queryByText('app')).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
