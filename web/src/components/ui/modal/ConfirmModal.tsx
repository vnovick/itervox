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
  danger: 'bg-theme-danger text-white hover:brightness-110',
  primary: 'bg-theme-accent text-white hover:brightness-110',
} as const;

// A 40px button with 12px corners (the Button primitive's `lg` size, kept
// as plain classes so this module does not pull in tailwind-merge).
const BUTTON =
  'inline-flex h-10 items-center justify-center rounded-[var(--radius-sm)] px-4 text-[15px] font-medium transition-[filter,background-color,opacity] disabled:opacity-50';

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
      className="max-w-md"
      ariaLabelledBy={titleId}
    >
      <p id={titleId} className="text-theme-text text-[17px] font-semibold tracking-[-0.01em]">
        {title}
      </p>
      {description && (
        <p className="text-theme-text-secondary mt-2.5 text-sm leading-relaxed">{description}</p>
      )}
      <ModalFooter>
        <button
          type="button"
          onClick={onClose}
          className={`${BUTTON} glass-chip text-theme-text hover:bg-theme-panel-strong`}
        >
          {cancelLabel}
        </button>
        <button
          type="button"
          onClick={onConfirm}
          disabled={isPending}
          className={`${BUTTON} font-semibold ${VARIANT_CLASSES[variant]}`}
        >
          {isPending ? (pendingLabel ?? confirmLabel) : confirmLabel}
        </button>
      </ModalFooter>
    </Modal>
  );
}
