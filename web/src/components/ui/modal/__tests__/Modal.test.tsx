import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { Modal } from '../index';
import { SlidePanel } from '../../SlidePanel/SlidePanel';

describe('Modal', () => {
  it('renders nothing when closed', () => {
    render(
      <Modal isOpen={false} onClose={vi.fn()} ariaLabel="Test">
        Content
      </Modal>,
    );
    expect(screen.queryByText('Content')).toBeNull();
  });

  it('renders children when open', () => {
    render(
      <Modal isOpen={true} onClose={vi.fn()} ariaLabel="Test">
        Content
      </Modal>,
    );
    expect(screen.getByText('Content')).toBeInTheDocument();
  });

  it('has role=dialog and aria-modal', () => {
    render(
      <Modal isOpen={true} onClose={vi.fn()} ariaLabel="Test">
        Test
      </Modal>,
    );
    const dialog = screen.getByRole('dialog');
    expect(dialog).toHaveAttribute('aria-modal', 'true');
  });

  it('shows close button by default', () => {
    render(
      <Modal isOpen={true} onClose={vi.fn()} ariaLabel="Test">
        Test
      </Modal>,
    );
    expect(screen.getByLabelText('Close')).toBeInTheDocument();
  });

  it('hides close button when showCloseButton=false', () => {
    render(
      <Modal isOpen={true} onClose={vi.fn()} showCloseButton={false} ariaLabel="Test">
        Test
      </Modal>,
    );
    expect(screen.queryByLabelText('Close')).toBeNull();
  });

  it('calls onClose when close button clicked', () => {
    const onClose = vi.fn();
    render(
      <Modal isOpen={true} onClose={onClose} ariaLabel="Test">
        Test
      </Modal>,
    );
    fireEvent.click(screen.getByLabelText('Close'));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('calls onClose on Escape key', () => {
    const onClose = vi.fn();
    render(
      <Modal isOpen={true} onClose={onClose} ariaLabel="Test">
        Test
      </Modal>,
    );
    fireEvent.keyDown(document, { key: 'Escape' });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('applies padded class when padded=true', () => {
    render(
      <Modal isOpen={true} onClose={vi.fn()} padded ariaLabel="Test">
        <span data-testid="inner">Padded</span>
      </Modal>,
    );
    // The p-6 is on the content wrapper div that wraps children
    const inner = screen.getByTestId('inner');
    // Walk up to find the div with p-6: inner → children wrapper (p-6)
    const wrapper = inner.closest('.p-6');
    expect(wrapper).not.toBeNull();
  });

  it('does not apply padding by default', () => {
    render(
      <Modal isOpen={true} onClose={vi.fn()} ariaLabel="Test">
        <span data-testid="inner">No pad</span>
      </Modal>,
    );
    const inner = screen.getByTestId('inner');
    const wrapper = inner.closest('.p-6');
    expect(wrapper).toBeNull();
  });

  it('applies custom className', () => {
    render(
      <Modal isOpen={true} onClose={vi.fn()} className="max-w-lg" ariaLabel="Test">
        Test
      </Modal>,
    );
    const dialog = screen.getByRole('dialog');
    expect(dialog.className).toContain('max-w-lg');
  });

  describe('CORE-067', () => {
    afterEach(() => {
      document.body.style.overflow = '';
    });

    it('exposes an accessible name', () => {
      const { unmount } = render(
        <Modal isOpen onClose={vi.fn()} ariaLabel="Add worker host">
          body
        </Modal>,
      );
      expect(screen.getByRole('dialog', { name: 'Add worker host' })).toBeInTheDocument();
      unmount();

      render(
        <Modal isOpen onClose={vi.fn()} ariaLabelledBy="modal-heading">
          <h2 id="modal-heading">Edit profile</h2>
        </Modal>,
      );
      expect(screen.getByRole('dialog', { name: 'Edit profile' })).toBeInTheDocument();
    });

    it('mounted closed does not change body overflow', () => {
      document.body.style.overflow = 'scroll';
      const { unmount } = render(
        <Modal isOpen={false} onClose={vi.fn()} ariaLabel="Closed">
          x
        </Modal>,
      );
      expect(document.body.style.overflow).toBe('scroll');
      unmount();
      expect(document.body.style.overflow).toBe('scroll');
    });

    it('closing over an open SlidePanel keeps body scroll locked', () => {
      document.body.style.overflow = 'auto';
      const tree = (modalOpen: boolean) => (
        <>
          <SlidePanel isOpen onClose={vi.fn()} title="Issue">
            <p>panel</p>
          </SlidePanel>
          <Modal isOpen={modalOpen} onClose={vi.fn()} ariaLabel="Confirm">
            <p>confirm</p>
          </Modal>
        </>
      );
      const { rerender } = render(tree(true));
      expect(document.body.style.overflow).toBe('hidden');
      rerender(tree(false));
      expect(document.body.style.overflow).toBe('hidden');
    });
  });
});
