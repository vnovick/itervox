import { useUIStore } from '../../../store/uiStore';
import { Modal } from '../../ui/modal';
import { NewTaskForm } from './NewTaskForm';

/** #251 — the New task dialog; open state lives in uiStore. */
export function NewTaskDialog() {
  const open = useUIStore((s) => s.newTaskOpen);
  const setOpen = useUIStore((s) => s.setNewTaskOpen);
  const close = () => {
    setOpen(false);
  };
  return (
    <Modal
      isOpen={open}
      onClose={close}
      ariaLabelledBy="new-task-title"
      className="max-w-[640px]"
      padded
    >
      <h2 id="new-task-title" className="text-theme-text mb-1 pr-12 text-xl font-bold">
        New task
      </h2>
      <p className="text-theme-text-secondary mb-4 pr-12 text-xs leading-relaxed">
        A one-off task with its own instructions. A local issue lives in{' '}
        <code className="text-theme-text font-mono">.itervox/issues/</code> and never reaches the
        tracker.
      </p>
      {open && <NewTaskForm onDone={close} />}
    </Modal>
  );
}
