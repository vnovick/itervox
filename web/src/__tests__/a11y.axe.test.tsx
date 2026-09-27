// M5-B1 — automated axe-core pass over the components changed by
// CORE-066 (Toast), CORE-067 (Modal / SlidePanel) and CORE-068 (board card,
// list row).
import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, render } from '@testing-library/react';
import { DndContext, KeyboardSensor, useSensor, useSensors } from '@dnd-kit/core';
import { axeViolations } from '../test/axe';
import Toast from '../components/common/Toast';
import { useToastStore } from '../store/toastStore';
import { Modal } from '../components/ui/modal';
import { SlidePanel } from '../components/ui/SlidePanel/SlidePanel';
import DraggableCard from '../components/itervox/BoardColumn/DraggableCard';
import { ListView } from '../pages/Dashboard/components/ListView';
import { makeIssue } from '../test/fixtures/issues';

vi.mock('../queries/issues', () => ({
  useCancelIssue: () => ({ mutate: vi.fn(), isPending: false }),
  useResumeIssue: () => ({ mutate: vi.fn(), isPending: false }),
}));

afterEach(() => {
  for (const t of useToastStore.getState()._timers.values()) clearTimeout(t);
  useToastStore.setState({ toasts: [], _timers: new Map() });
  document.body.style.overflow = '';
});

describe('axe', () => {
  it('the harness flags an unnamed dialog (sanity check)', async () => {
    const { container } = render(
      <div role="dialog" aria-modal="true">
        <button type="button">x</button>
      </div>,
    );
    const violations = await axeViolations(container);
    expect(violations.some((v) => v.startsWith('aria-dialog-name:'))).toBe(true);
  });

  it('Toast regions with success, info and a deduped error', async () => {
    const { container } = render(<Toast />);
    act(() => {
      const { addToast } = useToastStore.getState();
      addToast('Saved', 'success');
      addToast('Heads up', 'info');
      addToast('Failed', 'error');
      addToast('Failed', 'error');
    });
    expect(await axeViolations(container)).toEqual([]);
  });

  it('Modal over SlidePanel', async () => {
    const { baseElement } = render(
      <>
        <SlidePanel isOpen onClose={vi.fn()} title="ENG-1">
          <p>panel body</p>
        </SlidePanel>
        <Modal isOpen onClose={vi.fn()} ariaLabel="Confirm discard">
          <p>Discard the workspace?</p>
        </Modal>
      </>,
    );
    expect(await axeViolations(baseElement)).toEqual([]);
  });

  it('board card with drag handle and review badge', async () => {
    function Board() {
      const sensors = useSensors(useSensor(KeyboardSensor));
      return (
        <DndContext sensors={sensors}>
          <DraggableCard
            issue={makeIssue({ identifier: 'ENG-1', title: 'Card', url: 'https://example.com/1' })}
            isBeingDragged={false}
            shouldCollapse={false}
            onSelect={vi.fn()}
            runningKindByIdentifier={{ 'ENG-1': 'reviewer' }}
            commentCountByIdentifier={{ 'ENG-1': 1 }}
          />
        </DndContext>
      );
    }
    const { baseElement } = render(<Board />);
    expect(await axeViolations(baseElement)).toEqual([]);
  });

  it('list view rows', async () => {
    const { container } = render(
      <ListView
        issues={[makeIssue({ identifier: 'ENG-1', title: 'Row' })]}
        onSelect={vi.fn()}
        availableProfiles={[]}
        onProfileChange={vi.fn()}
      />,
    );
    expect(await axeViolations(container)).toEqual([]);
  });
});
