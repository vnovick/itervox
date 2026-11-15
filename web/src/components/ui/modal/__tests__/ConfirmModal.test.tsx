import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { ConfirmModal } from '../ConfirmModal';

describe('ConfirmModal', () => {
  it('renders nothing when closed', () => {
    render(<ConfirmModal isOpen={false} onClose={vi.fn()} onConfirm={vi.fn()} title="Delete?" />);
    expect(screen.queryByText('Delete?')).toBeNull();
  });

  it('renders title and description when open', () => {
    render(
      <ConfirmModal
        isOpen={true}
        onClose={vi.fn()}
        onConfirm={vi.fn()}
        title="Delete item?"
        description="This action cannot be undone."
      />,
    );
    expect(screen.getByText('Delete item?')).toBeInTheDocument();
    expect(screen.getByText('This action cannot be undone.')).toBeInTheDocument();
  });

  it('shows an optional context eyebrow above the title', () => {
    const { rerender } = render(
      <ConfirmModal isOpen onClose={vi.fn()} onConfirm={vi.fn()} title="Pause?" />,
    );
    expect(screen.queryByTestId('confirm-context')).toBeNull();
    rerender(
      <ConfirmModal
        isOpen
        onClose={vi.fn()}
        onConfirm={vi.fn()}
        title="Pause?"
        context="Dashboard · status strip"
      />,
    );
    const eyebrow = screen.getByTestId('confirm-context');
    expect(eyebrow).toHaveTextContent('Dashboard · status strip');
    expect(eyebrow).toHaveClass('eyebrow');
    // The eyebrow comes before the title in reading order.
    expect(
      eyebrow.compareDocumentPosition(screen.getByText('Pause?')) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it('calls onClose when Cancel is clicked', () => {
    const onClose = vi.fn();
    render(<ConfirmModal isOpen={true} onClose={onClose} onConfirm={vi.fn()} title="Delete?" />);
    fireEvent.click(screen.getByText('Cancel'));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('calls onConfirm when confirm button is clicked', () => {
    const onConfirm = vi.fn();
    render(
      <ConfirmModal
        isOpen={true}
        onClose={vi.fn()}
        onConfirm={onConfirm}
        title="Delete?"
        confirmLabel="Yes, delete"
      />,
    );
    fireEvent.click(screen.getByText('Yes, delete'));
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it('shows pending label when isPending', () => {
    render(
      <ConfirmModal
        isOpen={true}
        onClose={vi.fn()}
        onConfirm={vi.fn()}
        title="Delete?"
        confirmLabel="Delete"
        pendingLabel="Deleting…"
        isPending={true}
      />,
    );
    expect(screen.getByText('Deleting…')).toBeInTheDocument();
    expect(screen.queryByText('Delete')).toBeNull();
  });

  it('disables confirm button when isPending', () => {
    render(
      <ConfirmModal
        isOpen={true}
        onClose={vi.fn()}
        onConfirm={vi.fn()}
        title="Delete?"
        isPending={true}
      />,
    );
    expect(screen.getByText('Confirm')).toBeDisabled();
  });

  it('uses custom cancel label', () => {
    render(
      <ConfirmModal
        isOpen={true}
        onClose={vi.fn()}
        onConfirm={vi.fn()}
        title="Delete?"
        cancelLabel="Never mind"
      />,
    );
    expect(screen.getByText('Never mind')).toBeInTheDocument();
  });

  it('renders danger variant by default', () => {
    render(
      <ConfirmModal
        isOpen={true}
        onClose={vi.fn()}
        onConfirm={vi.fn()}
        title="Delete?"
        confirmLabel="Delete"
      />,
    );
    const btn = screen.getByText('Delete');
    expect(btn.className).toContain('bg-theme-danger');
  });

  it('renders primary variant', () => {
    render(
      <ConfirmModal
        isOpen={true}
        onClose={vi.fn()}
        onConfirm={vi.fn()}
        title="Save?"
        confirmLabel="Save"
        variant="primary"
      />,
    );
    const btn = screen.getByText('Save');
    expect(btn.className).toContain('bg-theme-accent');
  });
});
