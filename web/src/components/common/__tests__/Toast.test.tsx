import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { act, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import Toast from '../Toast';
import { Modal } from '../../ui/modal';
import { useToastStore } from '../../../store/toastStore';

function reset() {
  for (const t of useToastStore.getState()._timers.values()) clearTimeout(t);
  useToastStore.setState({ toasts: [], _timers: new Map() });
}

describe('Toast', () => {
  beforeEach(reset);
  afterEach(reset);

  it('success uses role=status and error uses role=alert', () => {
    render(<Toast />);
    act(() => {
      useToastStore.getState().addToast('Comment posted.', 'success');
      useToastStore.getState().addToast('Something failed', 'error');
      useToastStore.getState().addToast('Heads up', 'info');
    });
    const status = screen.getByRole('status');
    const alert = screen.getByRole('alert');
    expect(status).toHaveAttribute('aria-live', 'polite');
    expect(within(status).getByText('Comment posted.')).toBeInTheDocument();
    expect(within(status).getByText('Heads up')).toBeInTheDocument();
    expect(within(alert).getByText('Something failed')).toBeInTheDocument();
    expect(within(alert).queryByText('Comment posted.')).toBeNull();
    // Individual toasts are not themselves live regions (no nested alerts).
    expect(screen.getAllByRole('alert')).toHaveLength(1);
    expect(screen.getAllByRole('status')).toHaveLength(1);
  });

  it('live regions stay mounted when the queue is empty', () => {
    render(<Toast />);
    const status = screen.getByRole('status');
    const alert = screen.getByRole('alert');
    act(() => {
      useToastStore.getState().addToast('ok', 'success');
    });
    act(() => {
      useToastStore.getState().removeToast(useToastStore.getState().toasts[0].id);
    });
    // Same DOM nodes: the regions were never unmounted and remounted.
    expect(screen.getByRole('status')).toBe(status);
    expect(screen.getByRole('alert')).toBe(alert);
  });

  it('shows the dedupe counter', () => {
    render(<Toast />);
    act(() => {
      useToastStore.getState().addToast('Rate limited', 'error');
      useToastStore.getState().addToast('Rate limited', 'error');
    });
    expect(within(screen.getByRole('alert')).getByText('×2')).toBeInTheDocument();
  });

  it('container z-index exceeds Modal', () => {
    render(
      <>
        <Modal isOpen onClose={() => undefined} ariaLabel="Test dialog">
          body
        </Modal>
        <Toast />
      </>,
    );
    const modalRoot = screen.getByRole('dialog').parentElement;
    const modalZ = /\bz-(\d+)\b/.exec(modalRoot?.className ?? '')?.[1];
    expect(modalZ).toBeDefined();
    const container = screen.getByTestId('toast-container');
    const toastZ = Number(container.style.zIndex);
    expect(toastZ).toBeGreaterThan(Number(modalZ));
  });

  it('dismisses with the keyboard (button and Escape)', async () => {
    const user = userEvent.setup();
    render(<Toast />);
    act(() => {
      useToastStore.getState().addToast('first', 'error');
      useToastStore.getState().addToast('second', 'error');
    });
    const buttons = screen.getAllByRole('button', { name: 'Dismiss notification' });
    buttons[0].focus();
    await user.keyboard('{Enter}');
    expect(screen.queryByText('first')).toBeNull();

    screen.getByRole('button', { name: 'Dismiss notification' }).focus();
    await user.keyboard('{Escape}');
    expect(screen.queryByText('second')).toBeNull();
  });

  // M5-close — dismissing a focused toast used to drop focus to <body>.
  it('dismissing the focused toast moves focus to the next toast, then back to where it came from', async () => {
    const user = userEvent.setup();
    render(
      <main>
        <button type="button">Before</button>
        <Toast />
      </main>,
    );
    act(() => {
      useToastStore.getState().addToast('First failure', 'error');
      useToastStore.getState().addToast('Second failure', 'error');
    });
    const before = screen.getByRole('button', { name: 'Before' });
    act(() => {
      before.focus();
    });
    await user.tab();
    const dismiss = screen.getAllByRole('button', { name: 'Dismiss notification' });
    expect(dismiss[0]).toHaveFocus();
    await user.keyboard('{Enter}');
    // One toast left: focus lands on its dismiss button.
    const remaining = screen.getByRole('button', { name: 'Dismiss notification' });
    expect(remaining).toHaveFocus();
    await user.keyboard('{Enter}');
    expect(screen.queryByRole('button', { name: 'Dismiss notification' })).toBeNull();
    expect(before).toHaveFocus();
  });

  it('Escape on a focused toast with no origin falls back to the main landmark', async () => {
    const user = userEvent.setup();
    render(
      <main>
        <Toast />
      </main>,
    );
    act(() => {
      useToastStore.getState().addToast('Only failure', 'error');
    });
    await user.tab();
    expect(screen.getByRole('button', { name: 'Dismiss notification' })).toHaveFocus();
    await user.keyboard('{Escape}');
    expect(screen.getByRole('main')).toHaveFocus();
  });
});
