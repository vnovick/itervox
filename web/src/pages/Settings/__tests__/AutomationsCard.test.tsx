import type { ReactNode } from 'react';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AutomationsCard } from '../AutomationsCard';
import { SUGGESTED_AUTOMATIONS } from '../automations/suggestedAutomations';

// The AutomationFormModal embeds a TestFireControl that uses useMutation.
// Tests need a QueryClientProvider; wrap renders in this helper.
function withQueryClient(children: ReactNode) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}
import { MSG_AUTOMATIONS_DUPLICATE_ID, MSG_AUTOMATIONS_SAVE_SUCCESS } from '../automationMessages';
import type { AutomationDef } from '../../../types/schemas';

const existingQaAutomation: AutomationDef = {
  id: 'qa-ready',
  enabled: true,
  profile: 'qa',
  trigger: { type: 'cron', cron: '0 */2 * * *', timezone: 'UTC' },
  filter: { states: ['Ready for QA'] },
};

describe('AutomationsCard', () => {
  it('renders suggested automation templates for the built-in examples', () => {
    render(
      withQueryClient(
        <AutomationsCard
          automations={[]}
          availableProfiles={['input-responder', 'qa', 'pm']}
          availableStates={['Backlog', 'Ready for QA']}
          availableLabels={['triage', 'qa']}
          onSave={vi.fn().mockResolvedValue(true)}
        />,
      ),
    );

    expect(screen.getByText('Input Responder')).toBeInTheDocument();
    expect(screen.getByText('QA Validation')).toBeInTheDocument();
    expect(screen.getByText('PM Backlog Review')).toBeInTheDocument();
    expect(screen.getByText('Dependency Readiness')).toBeInTheDocument();
    expect(screen.getByText('Release Captain')).toBeInTheDocument();
    expect(screen.getAllByText('Use Template')).toHaveLength(SUGGESTED_AUTOMATIONS.length);
  });

  it('disables templates whose required profile is unavailable', async () => {
    const user = userEvent.setup();

    render(
      withQueryClient(
        <AutomationsCard
          automations={[]}
          availableProfiles={['qa']}
          availableStates={['Backlog', 'Ready for QA']}
          availableLabels={['triage', 'qa']}
          onSave={vi.fn().mockResolvedValue(true)}
        />,
      ),
    );

    const inputResponder = screen.getByRole('button', { name: /Input Responder/i });
    expect(inputResponder).toBeDisabled();

    await user.click(inputResponder);

    expect(screen.queryByText(/Use "Input Responder" Template/i)).not.toBeInTheDocument();
    expect(
      screen.getByText(
        (_, element) =>
          element?.textContent === 'Create and enable the input-responder profile first.',
      ),
    ).toBeInTheDocument();
  });

  // MED-3 regression guard: the saveAutomations dup-ID guard path must surface
  // a visible error and skip onSave.
  it('surfaces an error when editing would produce duplicate automation IDs', async () => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(true);

    // Two existing entries with distinct IDs; the user will try to rename one
    // to match the other.
    render(
      withQueryClient(
        <AutomationsCard
          automations={[existingQaAutomation, { ...existingQaAutomation, id: 'qa-second' }]}
          availableProfiles={['qa']}
          availableStates={['Ready for QA']}
          availableLabels={['qa']}
          onSave={onSave}
        />,
      ),
    );

    // Open the edit modal for "qa-second".
    const editButtons = screen.getAllByRole('button', { name: /edit/i });
    await user.click(editButtons[1]);

    // Rewrite the ID to collide.
    const idInput = screen.getByLabelText(/Automation ID/i);
    await user.clear(idInput);
    await user.type(idInput, 'qa-ready');

    // Save.
    const saveButton = screen.getByRole('button', { name: /Save Changes/i });
    await user.click(saveButton);

    expect(onSave).not.toHaveBeenCalled();
    expect(screen.getByText(MSG_AUTOMATIONS_DUPLICATE_ID)).toBeInTheDocument();
  }, 15000);

  // MED-3 regression guard: successful save must surface a visible success
  // message so users know WORKFLOW.md was written and the reload is coming.
  it('shows a success banner after a successful save', async () => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(true);

    render(
      withQueryClient(
        <AutomationsCard
          automations={[existingQaAutomation]}
          availableProfiles={['qa']}
          availableStates={['Ready for QA']}
          availableLabels={['qa']}
          onSave={onSave}
        />,
      ),
    );

    const editButtons = screen.getAllByRole('button', { name: /edit/i });
    await user.click(editButtons[0]);
    // Don't rename — just save through unchanged.
    const saveButton = screen.getByRole('button', { name: /Save Changes/i });
    await user.click(saveButton);

    expect(onSave).toHaveBeenCalledTimes(1);
    expect(await screen.findByText(MSG_AUTOMATIONS_SAVE_SUCCESS)).toBeInTheDocument();
  }, 15000);
});
