import { useCallback, useLayoutEffect, useRef, useEffect, useState, startTransition } from 'react';
import { useDraggable } from '@dnd-kit/core';
import { CSS } from '@dnd-kit/utilities';
import IssueCard from '../IssueCard';
import type { TrackerIssue, ProfileDef } from '../../../types/schemas';

// Identifier whose drag handle held focus when its card unmounted (a drop
// into another column re-mounts the card there). The re-mounted card takes
// focus back so a keyboard drag does not end on <body>.
let focusHandoff: string | null = null;

/**
 * One draggable item in a BoardColumn. While being dragged, the source-cell
 * placeholder collapses to keep neighbouring cards from snapping; on drop it
 * restores. Extracted from `BoardColumn.tsx` (T-20) so the parent can stay
 * focused on column-level concerns and so this card can be tested in
 * isolation if needed.
 */
export default function DraggableCard({
  issue,
  isBeingDragged,
  shouldCollapse,
  onSelect,
  availableProfiles,
  profileDefs,
  runningBackendByIdentifier,
  defaultBackend,
  onProfileChange,
  onDispatch,
  runningKindByIdentifier,
  commentCountByIdentifier,
  inputRequiredStaleByIdentifier,
  retryAttemptByIdentifier,
  maxRetries,
  syncingIdentifiers,
}: {
  issue: TrackerIssue;
  /** True when THIS card is being dragged */
  isBeingDragged: boolean;
  /** True when the dragged card has left the source column (collapse the placeholder) */
  shouldCollapse: boolean;
  onSelect: (id: string) => void;
  availableProfiles?: string[];
  profileDefs?: Record<string, ProfileDef>;
  runningBackendByIdentifier?: Record<string, string>;
  defaultBackend?: string;
  onProfileChange?: (identifier: string, profile: string) => void;
  onDispatch?: (identifier: string) => void;
  /** T-6 surface: latest run kind ("worker" | "reviewer") per identifier. */
  runningKindByIdentifier?: Record<string, string>;
  /** T-6 surface: review-comment count per identifier. */
  commentCountByIdentifier?: Record<string, number>;
  /** Gap A: stale + age data for input_required entries, by identifier. */
  inputRequiredStaleByIdentifier?: Record<string, { stale: boolean; ageMinutes?: number }>;
  /** G: retry attempt number per identifier when an issue is mid-retry. */
  retryAttemptByIdentifier?: Record<string, number>;
  /** G: snapshot's max_retries; "M" denominator for the retry pill. 0 = unlimited. */
  maxRetries?: number;
  /** outbox Task 4: identifiers with a pending write-ahead-outbox entry (snapshot.outboxSyncing). */
  syncingIdentifiers?: ReadonlySet<string>;
}) {
  const cardRef = useRef<HTMLDivElement>(null);
  const [measuredHeight, setMeasuredHeight] = useState(72);

  const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, isDragging } =
    useDraggable({
      id: issue.identifier,
      data: { issue },
    });

  const style =
    transform && !isDragging ? { transform: CSS.Translate.toString(transform) } : undefined;

  // CORE-081 — track the card height with a ResizeObserver instead of reading
  // offsetHeight after every render. The observer is (re)attached only when the
  // real card is mounted (not while the drag placeholder stands in for it), so
  // the placeholder's first isDragging render reads the last observed height.
  // Zero heights are ignored: a detached node reports 0 and would collapse the
  // placeholder. Without ResizeObserver (old engines) the card is measured once
  // per mount instead.
  useEffect(() => {
    const node = cardRef.current;
    if (isDragging || !node) return;
    if (typeof ResizeObserver === 'undefined') {
      const h = node.offsetHeight;
      if (h > 0) setMeasuredHeight(h);
      return;
    }
    const observer = new ResizeObserver((entries) => {
      const h = entries[entries.length - 1]?.contentRect.height ?? 0;
      if (h > 0) {
        startTransition(() => {
          setMeasuredHeight(h);
        });
      }
    });
    observer.observe(node);
    return () => {
      observer.disconnect();
    };
  }, [isDragging]);

  // CORE-068 — dnd-kit's KeyboardSensor starts a drag on Space/Enter. With an
  // activator node registered it only does so when the key event's target IS
  // that node (core 6.3.1, KeyboardSensor.activators). So the listeners stay
  // on the whole card (pointer/touch drag from anywhere keeps working) while
  // the role=button attributes and the activator ref move to a dedicated drag
  // handle. The title button and nested controls get their keys back.

  const handleNodeRef = useRef<HTMLElement | null>(null);
  const placeholderHandleRef = useRef<HTMLButtonElement>(null);
  const setHandleRef = useCallback(
    (node: HTMLElement | null) => {
      handleNodeRef.current = node;
      setActivatorNodeRef(node);
    },
    [setActivatorNodeRef],
  );

  // Layout effects: the cleanup runs before React removes the old card's DOM
  // (so it still sees the focused handle) and the mount runs after the new
  // card's handle ref is attached, in the same commit.
  useLayoutEffect(() => {
    if (focusHandoff === issue.identifier) {
      focusHandoff = null;
      const active = document.activeElement;
      if (!active || active === document.body) handleNodeRef.current?.focus();
    }
    return () => {
      const active = document.activeElement;
      if (active && (active === handleNodeRef.current || active === placeholderHandleRef.current)) {
        focusHandoff = issue.identifier;
      }
    };
  }, [issue.identifier]);

  // M5-B1 follow-up — while lifted, the card's cell is a placeholder and the
  // real handle unmounts, which dropped keyboard focus to <body> for the
  // whole drag. The placeholder keeps a visually hidden stand-in handle
  // (same activator ref and dnd-kit attributes) and focus moves to it; on
  // drop dnd-kit's RestoreFocus returns focus to the card's own handle.
  useEffect(() => {
    if (!isDragging) return;
    const active = document.activeElement;
    if (!active || active === document.body) placeholderHandleRef.current?.focus();
  }, [isDragging]);

  if (isDragging) {
    const h = measuredHeight;
    return (
      <div
        ref={setNodeRef}
        {...listeners}
        className={`border-theme-line-strong overflow-hidden rounded-lg border-2 border-dashed transition-all duration-300 ease-in-out ${
          shouldCollapse ? 'my-0 max-h-0 border-0 opacity-0' : 'opacity-100'
        }`}
        style={shouldCollapse ? undefined : { height: h }}
      >
        <button
          type="button"
          ref={(node) => {
            placeholderHandleRef.current = node;
            setHandleRef(node);
          }}
          {...attributes}
          aria-label={`Move ${issue.identifier}`}
          data-testid="issue-card-drag-handle-placeholder"
          className="sr-only"
        />
      </div>
    );
  }

  return (
    <div
      ref={(node) => {
        setNodeRef(node);
        cardRef.current = node;
      }}
      style={style}
      {...listeners}
    >
      <IssueCard
        issue={issue}
        isDragging={isBeingDragged}
        onSelect={onSelect}
        availableProfiles={availableProfiles}
        profileDefs={profileDefs}
        runningBackend={runningBackendByIdentifier?.[issue.identifier]}
        defaultBackend={defaultBackend}
        onProfileChange={onProfileChange}
        onDispatch={onDispatch}
        runningKind={runningKindByIdentifier?.[issue.identifier]}
        commentCount={commentCountByIdentifier?.[issue.identifier]}
        inputRequiredStale={inputRequiredStaleByIdentifier?.[issue.identifier]?.stale}
        inputRequiredAgeMinutes={inputRequiredStaleByIdentifier?.[issue.identifier]?.ageMinutes}
        retryAttempt={retryAttemptByIdentifier?.[issue.identifier]}
        maxRetries={maxRetries}
        syncing={syncingIdentifiers?.has(issue.identifier)}
        dragHandleAttributes={attributes}
        dragHandleRef={setHandleRef}
      />
    </div>
  );
}
