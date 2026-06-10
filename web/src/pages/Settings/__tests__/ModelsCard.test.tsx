import { describe, expect, it, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ModelsCard } from '../ModelsCard';
import { useToastStore } from '../../../store/toastStore';

// authedFetch is the project's bearer-token wrapper. Mock it so the test
// drives the Refresh handler without booting the real auth stack.
vi.mock('../../../auth/authedFetch', () => ({
  authedFetch: vi.fn(),
}));

import { authedFetch } from '../../../auth/authedFetch';

const sampleModels = {
  claude: [
    { id: 'claude-sonnet-4-6', label: 'Sonnet 4.6' },
    { id: 'claude-haiku-4-5-20251001', label: 'Haiku 4.5' },
  ],
  codex: [{ id: 'gpt-5.3-codex', label: 'GPT-5.3-Codex' }],
};

describe('ModelsCard', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useToastStore.setState({ toasts: [] });
  });

  it('renders the available models grouped by backend', () => {
    render(<ModelsCard availableModels={sampleModels} />);
    expect(screen.getByText('claude')).toBeInTheDocument();
    expect(screen.getByText('codex')).toBeInTheDocument();
    expect(screen.getByText('claude-sonnet-4-6')).toBeInTheDocument();
    expect(screen.getByText('gpt-5.3-codex')).toBeInTheDocument();
  });

  it('renders the empty-state hint when no models are configured', () => {
    render(<ModelsCard availableModels={{}} />);
    expect(screen.getByText(/No models discovered yet/i)).toBeInTheDocument();
  });

  it('POSTs to /api/v1/settings/models/refresh with backend=all on the global button', async () => {
    vi.mocked(authedFetch).mockResolvedValueOnce(
      new Response(JSON.stringify({ ok: true, models: sampleModels }), { status: 200 }),
    );
    const user = userEvent.setup();
    render(<ModelsCard availableModels={sampleModels} />);

    await user.click(screen.getByTestId('models-refresh-all'));

    await waitFor(() => {
      expect(authedFetch).toHaveBeenCalledTimes(1);
    });
    const [url, init] = vi.mocked(authedFetch).mock.calls[0];
    expect(url).toBe('/api/v1/settings/models/refresh');
    expect(init?.method).toBe('POST');
    const body = init?.body;
    if (typeof body !== 'string') throw new Error('expected string body');
    expect(JSON.parse(body)).toEqual({ backend: 'all' });
  });

  it('POSTs backend=claude when the per-backend refresh link is clicked', async () => {
    vi.mocked(authedFetch).mockResolvedValueOnce(
      new Response(JSON.stringify({ ok: true, models: sampleModels }), { status: 200 }),
    );
    const user = userEvent.setup();
    render(<ModelsCard availableModels={sampleModels} />);

    await user.click(screen.getByTestId('models-refresh-claude'));

    await waitFor(() => {
      expect(authedFetch).toHaveBeenCalledTimes(1);
    });
    const [, init] = vi.mocked(authedFetch).mock.calls[0];
    const body = init?.body;
    if (typeof body !== 'string') throw new Error('expected string body');
    expect(JSON.parse(body)).toEqual({ backend: 'claude' });
  });

  it('surfaces a success toast on 200', async () => {
    vi.mocked(authedFetch).mockResolvedValueOnce(
      new Response(JSON.stringify({ ok: true, models: sampleModels }), { status: 200 }),
    );
    const user = userEvent.setup();
    render(<ModelsCard availableModels={sampleModels} />);

    await user.click(screen.getByTestId('models-refresh-all'));

    await waitFor(() => {
      const toasts = useToastStore.getState().toasts;
      expect(toasts.some((t) => /Refreshed/i.test(t.message))).toBe(true);
    });
  });

  it('surfaces an info toast on 501 not_implemented', async () => {
    vi.mocked(authedFetch).mockResolvedValueOnce(
      new Response(JSON.stringify({ error: { code: 'not_implemented' } }), { status: 501 }),
    );
    const user = userEvent.setup();
    render(<ModelsCard availableModels={sampleModels} />);

    await user.click(screen.getByTestId('models-refresh-all'));

    await waitFor(() => {
      const toasts = useToastStore.getState().toasts;
      expect(toasts.some((t) => /does not implement/i.test(t.message))).toBe(true);
    });
  });

  it('surfaces an error toast on non-2xx, non-501', async () => {
    vi.mocked(authedFetch).mockResolvedValueOnce(new Response('boom', { status: 500 }));
    const user = userEvent.setup();
    render(<ModelsCard availableModels={sampleModels} />);

    await user.click(screen.getByTestId('models-refresh-all'));

    await waitFor(() => {
      const toasts = useToastStore.getState().toasts;
      expect(toasts.some((t) => /Refresh failed/i.test(t.message))).toBe(true);
    });
  });

  it('surfaces an error toast on network failure', async () => {
    vi.mocked(authedFetch).mockRejectedValueOnce(new Error('network down'));
    const user = userEvent.setup();
    render(<ModelsCard availableModels={sampleModels} />);

    await user.click(screen.getByTestId('models-refresh-all'));

    await waitFor(() => {
      const toasts = useToastStore.getState().toasts;
      expect(toasts.some((t) => /network down/i.test(t.message))).toBe(true);
    });
  });

  it('disables all refresh buttons while a request is in flight', async () => {
    let resolveFetch!: (resp: Response) => void;
    vi.mocked(authedFetch).mockImplementationOnce(
      () =>
        new Promise<Response>((res) => {
          resolveFetch = res;
        }),
    );
    const user = userEvent.setup();
    render(<ModelsCard availableModels={sampleModels} />);

    const button = screen.getByTestId('models-refresh-all');
    await user.click(button);

    expect(button).toBeDisabled();
    expect(screen.getByTestId('models-refresh-claude')).toBeDisabled();

    resolveFetch(new Response(JSON.stringify({ ok: true, models: sampleModels }), { status: 200 }));

    await waitFor(() => {
      expect(button).not.toBeDisabled();
    });
  });
});
