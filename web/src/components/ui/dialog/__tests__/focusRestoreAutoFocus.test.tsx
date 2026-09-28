// M5-close (verifier MED) — a modal whose content uses `autoFocus` must
// still return focus to the control that opened it. React focuses an
// autoFocus input during commit, before the trap's effect ran, so the trap
// used to record the modal's own input as the "opener" and focus fell to
// <body> on close.
import { useState, type ReactElement } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AddSSHHostModal } from '../../../../pages/Settings/AddSSHHostModal';
import { AutomationFormModal } from '../../../../pages/Settings/automations/AutomationFormModal';
import { ProfileFormModal } from '../../../../pages/Settings/profiles/ProfileFormModal';
import { emptyProfileValues } from '../../../../pages/Settings/profiles/profileForm';
import type { AutomationFormValues } from '../../../../pages/Settings/automations/automationForm';

const automationValues: AutomationFormValues = {
  id: 'a1',
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
  limit: '1',
  autoResume: false,
  instructions: '',
  switchToProfile: '',
  switchToBackend: '',
  cooldownMinutes: '',
  moveToState: '',
};

type ModalFactory = (open: boolean, onClose: () => void) => ReactElement;

const CASES: [string, ModalFactory][] = [
  [
    'AddSSHHostModal',
    (open, onClose) => (
      <AddSSHHostModal isOpen={open} onClose={onClose} onAdd={() => Promise.resolve(true)} />
    ),
  ],
  [
    'AutomationFormModal',
    (open, onClose) => (
      <AutomationFormModal
        isOpen={open}
        title="New automation"
        submitLabel="Save"
        initialValues={automationValues}
        availableProfiles={['reviewer']}
        availableStates={['Todo']}
        availableLabels={[]}
        onClose={onClose}
        onSubmit={vi.fn()}
      />
    ),
  ],
  [
    'ProfileFormModal',
    (open, onClose) => (
      <ProfileFormModal
        isOpen={open}
        mode="add"
        title="New profile"
        submitLabel="Save"
        initialValues={emptyProfileValues()}
        onClose={onClose}
        onSubmit={vi.fn()}
      />
    ),
  ],
];

function Harness({ modal }: { modal: ModalFactory }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button
        type="button"
        onClick={() => {
          setOpen(true);
        }}
      >
        Open it
      </button>
      {modal(open, () => {
        setOpen(false);
      })}
    </>
  );
}

describe('dialog focus restore with autoFocus content', () => {
  for (const [name, modal] of CASES) {
    it(`${name}: Escape returns focus to the opener`, async () => {
      const user = userEvent.setup();
      render(
        <QueryClientProvider client={new QueryClient()}>
          <Harness modal={modal} />
        </QueryClientProvider>,
      );
      const opener = screen.getByRole('button', { name: 'Open it' });
      // Open via the keyboard, as an operator would.
      act(() => {
        opener.focus();
      });
      await user.keyboard('{Enter}');
      const dialog = await screen.findByRole('dialog');
      // autoFocus moved focus into the modal.
      expect(dialog.contains(document.activeElement)).toBe(true);
      await user.keyboard('{Escape}');
      await act(async () => {
        await Promise.resolve();
      });
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
      expect(opener).toHaveFocus();
    });
  }
});
