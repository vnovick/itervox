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
      <h2 id="new-task-title" className="text-theme-text mb-1 text-base font-semibold">
        New task
      </h2>
      <p className="text-theme-muted mb-4 text-[12px]">
        A one-off task with its own instructions. A local issue lives in .itervox/issues/ and never
        reaches the tracker.
      </p>
      {open && <NewTaskForm onDone={close} />}
    </Modal>
  );
}
