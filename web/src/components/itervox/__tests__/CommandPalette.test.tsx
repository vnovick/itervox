// CORE-095 — the command palette.
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { useState } from 'react';
import { CommandPalette } from '../CommandPalette';
import { useItervoxStore } from '../../../store/itervoxStore';
import { makeRunningRow, makeSnapshot } from '../../../test/fixtures/snapshots';

const mocks = vi.hoisted(() => ({ terminate: vi.fn(), cancel: vi.fn() }));
vi.mock('../../../queries/issues', () => ({
  useIssues: () => ({
    data: [
      { identifier: 'ENG-1', title: 'Fix the login', state: 'In Progress' },
      { identifier: 'ENG-2', title: 'Write docs', state: 'Todo' },
    ],
  }),
  useTerminateIssue: () => ({ mutate: mocks.terminate, isPending: false }),
  useCancelIssue: () => ({ mutate: mocks.cancel, isPending: false }),
}));

function Harness() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button
        type="button"
        onClick={() => {
          setOpen(true);
        }}
      >
        Open palette
      </button>
      <CommandPalette
        isOpen={open}
        onClose={() => {
          setOpen(false);
        }}
      />
    </>
  );
}

beforeEach(() => {
  mocks.terminate.mockReset();
  mocks.cancel.mockReset();
  useItervoxStore.setState({
    snapshot: makeSnapshot({ running: [makeRunningRow({ identifier: 'ENG-1' })] }),
    selectedIdentifier: null,
  });
});

function renderPalette() {
  return render(
    <MemoryRouter>
      <Harness />
    </MemoryRouter>,
  );
}

describe('CommandPalette', () => {
  it('Escape closes and restores focus to the opener', async () => {
    const user = userEvent.setup();
    renderPalette();
    const opener = screen.getByRole('button', { name: 'Open palette' });
    await user.click(opener);
    const input = await screen.findByRole('combobox', { name: /search commands/i });
    await waitFor(() => {
      expect(input).toHaveFocus();
    });
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog', { name: /command palette/i })).not.toBeInTheDocument();
    await waitFor(() => {
      expect(opener).toHaveFocus();
    });
  });

  it('jumps to an issue: typing its id and Enter selects it', async () => {
    const user = userEvent.setup();
    renderPalette();
    await user.click(screen.getByRole('button', { name: 'Open palette' }));
    await user.type(await screen.findByRole('combobox'), 'ENG-2');
    await user.keyboard('{Enter}');
    expect(useItervoxStore.getState().selectedIdentifier).toBe('ENG-2');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('terminate requires a confirm step before mutate is called', async () => {
    const user = userEvent.setup();
    renderPalette();
    await user.click(screen.getByRole('button', { name: 'Open palette' }));
    await user.type(await screen.findByRole('combobox'), 'discard eng-1');
    await user.keyboard('{Enter}');
    expect(mocks.terminate).not.toHaveBeenCalled();
    const confirm = screen.getByRole('button', { name: /yes, discard ENG-1/i });
    expect(confirm).toHaveFocus();
    await user.click(confirm);
    expect(mocks.terminate).toHaveBeenCalledWith('ENG-1');
  });

  it('pause also requires a confirm step, and Cancel backs out', async () => {
    const user = userEvent.setup();
    renderPalette();
    await user.click(screen.getByRole('button', { name: 'Open palette' }));
    await user.type(await screen.findByRole('combobox'), 'pause eng-1');
    await user.keyboard('{Enter}');
    await user.click(screen.getByRole('button', { name: /^cancel$/i }));
    expect(mocks.cancel).not.toHaveBeenCalled();
    expect(screen.getByRole('combobox')).toBeInTheDocument();
  });

  it('arrow keys move the active option (aria-activedescendant)', async () => {
    const user = userEvent.setup();
    renderPalette();
    await user.click(screen.getByRole('button', { name: 'Open palette' }));
    const input = await screen.findByRole('combobox');
    const first = input.getAttribute('aria-activedescendant');
    await user.keyboard('{ArrowDown}');
    expect(input.getAttribute('aria-activedescendant')).not.toBe(first);
    act(() => undefined);
  });

  it('a click on an option runs it; ArrowUp moves back; no-match shows a message', async () => {
    const user = userEvent.setup();
    renderPalette();
    await user.click(screen.getByRole('button', { name: 'Open palette' }));
    const input = await screen.findByRole('combobox');
    await user.keyboard('{ArrowDown}{ArrowDown}{ArrowUp}');
    const active = input.getAttribute('aria-activedescendant');
    expect(document.getElementById(active ?? '')).toHaveAttribute('aria-selected', 'true');
    await user.type(input, 'zzzz-nothing');
    expect(screen.getByText('No matching commands')).toBeInTheDocument();
    await user.clear(input);
    await user.type(input, 'ENG-1 Fix');
    await user.click(screen.getByRole('option', { name: /Open ENG-1/ }));
    expect(useItervoxStore.getState().selectedIdentifier).toBe('ENG-1');
  });
});
