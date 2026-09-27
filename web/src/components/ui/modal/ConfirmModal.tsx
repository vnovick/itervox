import { useId } from 'react';
import { Modal } from './index';
import { ModalFooter } from './ModalFooter';

interface ConfirmModalProps {
  isOpen: boolean;
  onClose: () => void;
  onConfirm: () => void;
  /** Title text. */
  title: string;
  /** Optional description below the title. */
  description?: string;
  /** Label for the confirm button. Default: 'Confirm'. */
  confirmLabel?: string;
  /** Label for the cancel button. Default: 'Cancel'. */
  cancelLabel?: string;
  /** Visual variant for the confirm button. Default: 'danger'. */
  variant?: 'danger' | 'primary';
  /** Whether the confirm action is in progress. */
  isPending?: boolean;
  /** Label to show while pending. */
  pendingLabel?: string;
}

const VARIANT_CLASSES = {
  danger: 'bg-theme-danger text-white hover:opacity-90',
  primary: 'bg-theme-accent text-white hover:opacity-90',
} as const;

/**
 * Standard confirmation modal with title, description, Cancel + Confirm buttons.
 * Replaces ad-hoc confirm patterns throughout the codebase.
 */
export function ConfirmModal({
  isOpen,
  onClose,
  onConfirm,
  title,
  description,
  confirmLabel = 'Confirm',
  cancelLabel = 'Cancel',
  variant = 'danger',
  isPending = false,
  pendingLabel,
}: ConfirmModalProps) {
  const titleId = useId();
  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      showCloseButton={false}
      padded
      className="max-w-sm"
      ariaLabelledBy={titleId}
    >
      <p id={titleId} className="text-theme-text text-sm font-semibold">
        {title}
      </p>
      {description && <p className="text-theme-muted mt-1 text-xs">{description}</p>}
      <ModalFooter>
        <button
          onClick={onClose}
          className="border-theme-line text-theme-text-secondary rounded-[var(--radius-sm)] border px-3.5 py-1.5 text-xs font-medium transition-colors hover:opacity-80"
        >
          {cancelLabel}
        </button>
        <button
          onClick={onConfirm}
          disabled={isPending}
          className={`rounded-[var(--radius-sm)] px-3.5 py-1.5 text-xs font-semibold transition-colors disabled:opacity-50 ${VARIANT_CLASSES[variant]}`}
        >
          {isPending ? (pendingLabel ?? confirmLabel) : confirmLabel}
        </button>
      </ModalFooter>
    </Modal>
  );
}
