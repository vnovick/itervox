import { StrictMode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { SlidePanel } from '../../SlidePanel/SlidePanel';
import { Modal } from '../../modal';

interface HarnessProps {
  panelOpen: boolean;
  modalOpen: boolean;
  onPanelClose?: () => void;
  onModalClose?: () => void;
}

function Harness({ panelOpen, modalOpen, onPanelClose, onModalClose }: HarnessProps) {
  return (
    <>
      <SlidePanel isOpen={panelOpen} onClose={onPanelClose ?? (() => undefined)} title="Panel">
        <button type="button">panel-a</button>
        <button type="button">panel-b</button>
      </SlidePanel>
      <Modal isOpen={modalOpen} onClose={onModalClose ?? (() => undefined)} ariaLabel="Confirm">
        <button type="button">modal-a</button>
        <button type="button">modal-b</button>
      </Modal>
    </>
  );
}

describe('overlay stack', () => {
  beforeEach(() => {
    document.body.style.overflow = 'auto';
  });
  afterEach(() => {
    document.body.style.overflow = '';
  });

  it('closing in reverse order restores the original overflow', () => {
    const { rerender } = render(<Harness panelOpen modalOpen={false} />);
    expect(document.body.style.overflow).toBe('hidden');
    rerender(<Harness panelOpen modalOpen />);
    expect(document.body.style.overflow).toBe('hidden');
    rerender(<Harness panelOpen modalOpen={false} />);
    expect(document.body.style.overflow).toBe('hidden');
    rerender(<Harness panelOpen={false} modalOpen={false} />);
    expect(document.body.style.overflow).toBe('auto');
  });

  it('unmounting the lower dialog first still restores overflow when the last owner releases', () => {
    const { rerender } = render(<Harness panelOpen modalOpen />);
    rerender(<Harness panelOpen={false} modalOpen />);
    // The Modal still owns a lock.
    expect(document.body.style.overflow).toBe('hidden');
    rerender(<Harness panelOpen={false} modalOpen={false} />);
    expect(document.body.style.overflow).toBe('auto');
  });

  it('StrictMode double effect run does not leak a lock', () => {
    const { rerender } = render(
      <StrictMode>
        <Harness panelOpen modalOpen />
      </StrictMode>,
    );
    expect(document.body.style.overflow).toBe('hidden');
    rerender(
      <StrictMode>
        <Harness panelOpen={false} modalOpen={false} />
      </StrictMode>,
    );
    expect(document.body.style.overflow).toBe('auto');
  });

  it('Escape closes only the top dialog', () => {
    const onPanelClose = vi.fn();
    const onModalClose = vi.fn();
    const { rerender } = render(
      <Harness panelOpen modalOpen onPanelClose={onPanelClose} onModalClose={onModalClose} />,
    );
    fireEvent.keyDown(document, { key: 'Escape' });
    expect(onModalClose).toHaveBeenCalledTimes(1);
    expect(onPanelClose).not.toHaveBeenCalled();

    // Once the Modal is gone the SlidePanel is the top layer again.
    rerender(
      <Harness
        panelOpen
        modalOpen={false}
        onPanelClose={onPanelClose}
        onModalClose={onModalClose}
      />,
    );
    fireEvent.keyDown(document, { key: 'Escape' });
    expect(onPanelClose).toHaveBeenCalledTimes(1);
    expect(onModalClose).toHaveBeenCalledTimes(1);
  });

  it('only the top dialog traps focus (Modal over SlidePanel: Tab cycles inside the Modal only)', async () => {
    render(<Harness panelOpen modalOpen />);
    const modal = screen.getByRole('dialog', { name: 'Confirm' });
    await waitFor(() => {
      expect(modal.contains(document.activeElement)).toBe(true);
    });

    // Tab from the Modal's last focusable wraps to its first.
    const modalB = screen.getByRole('button', { name: 'modal-b' });
    modalB.focus();
    fireEvent.keyDown(modalB, { key: 'Tab' });
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Close' }));

    // Focus that escaped into the lower SlidePanel is pulled back into the Modal
    // rather than cycling inside the panel.
    const panelB = screen.getByRole('button', { name: 'panel-b' });
    panelB.focus();
    fireEvent.keyDown(panelB, { key: 'Tab' });
    expect(modal.contains(document.activeElement)).toBe(true);
  });
});
