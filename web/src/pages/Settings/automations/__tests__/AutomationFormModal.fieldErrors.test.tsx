import { describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AutomationFormModal, type AutomationSubmitResult } from '../AutomationFormModal';
import type { AutomationFormValues } from '../automationForm';

const baseValues: AutomationFormValues = {
  id: 'my-automation',
  enabled: true,
  profile: 'reviewer',
  triggerType: 'cron',
  cron: '0 9 * * *',
  timezone: 'UTC',
  triggerState: '',
  matchMode: 'any',
  states: [],
  labelsAny: [],
  identifierRegex: '',
  inputContextRegex: '',
  maxAgeMinutes: '',
  // limit is a string in the form schema (textbox-validated as integer).
  limit: '1',
  autoResume: false,
  instructions: '',
  switchToProfile: '',
  switchToBackend: '',
  cooldownMinutes: '',
  moveToState: '',
};

function renderForm(onSubmit: (v: AutomationFormValues) => Promise<AutomationSubmitResult>) {
  // Wrap in QueryClientProvider — the embedded TestFireControl uses
  // useMutation, which requires a client even though the tests don't
  // exercise it.
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={qc}>
      <AutomationFormModal
        isOpen
        title="Test"
        submitLabel="Save"
        initialValues={baseValues}
        availableProfiles={['reviewer']}
        availableStates={['Todo', 'In Progress']}
        availableLabels={[]}
        onClose={vi.fn()}
        onSubmit={onSubmit}
      />
    </QueryClientProvider>,
  );
}

describe('AutomationFormModal — server field errors (T-34)', () => {
  it('pins a server fieldErrors entry to the matching input via RHF setError', async () => {
    const onSubmit = vi.fn(
      (): Promise<AutomationSubmitResult> =>
        Promise.resolve({
          ok: false,
          fieldErrors: { id: 'duplicate automation id "my-automation"' },
        }),
    );
    renderForm(onSubmit);

    fireEvent.click(screen.getByRole('button', { name: /save/i }));

    await waitFor(() => {
      expect(onSubmit).toHaveBeenCalledTimes(1);
    });
    // The error message is rendered as a sibling of the id input (role=alert
    // covers both client-side Zod errors and the server-pinned errors).
    const alerts = await screen.findAllByRole('alert');
    expect(alerts.some((el) => /duplicate automation id/i.test(el.textContent))).toBe(true);
  });

  it('keeps the modal open when the typed result is { ok: false } (vs closing on ok: true)', async () => {
    const onClose = vi.fn();
    const onSubmit = vi.fn(
      (): Promise<AutomationSubmitResult> =>
        Promise.resolve({
          ok: false,
          fieldErrors: { cron: 'invalid cron expression' },
        }),
    );
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    render(
      <QueryClientProvider client={qc}>
        <AutomationFormModal
          isOpen
          title="Test"
          submitLabel="Save"
          initialValues={baseValues}
          availableProfiles={['reviewer']}
          availableStates={[]}
          availableLabels={[]}
          onClose={onClose}
          onSubmit={onSubmit}
        />
      </QueryClientProvider>,
    );

    fireEvent.click(screen.getByRole('button', { name: /save/i }));
    await waitFor(() => {
      expect(onSubmit).toHaveBeenCalled();
    });
    expect(onClose).not.toHaveBeenCalled();
  });

  it('still treats a plain `true` return as success and closes the modal', async () => {
    const onClose = vi.fn();
    // eslint-disable-next-line @typescript-eslint/require-await
    const onSubmit = vi.fn(async (): Promise<AutomationSubmitResult> => true);
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
    });
    render(
      <QueryClientProvider client={qc}>
        <AutomationFormModal
          isOpen
          title="Test"
          submitLabel="Save"
          initialValues={baseValues}
          availableProfiles={['reviewer']}
          availableStates={[]}
          availableLabels={[]}
          onClose={onClose}
          onSubmit={onSubmit}
        />
      </QueryClientProvider>,
    );

    fireEvent.click(screen.getByRole('button', { name: /save/i }));
    await waitFor(() => {
      expect(onClose).toHaveBeenCalled();
    });
  });
});
