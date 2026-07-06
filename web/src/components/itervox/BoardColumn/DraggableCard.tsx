import { useRef, useEffect, useState, startTransition } from 'react';
import { useDraggable } from '@dnd-kit/core';
import { CSS } from '@dnd-kit/utilities';
import IssueCard from '../IssueCard';
import type { TrackerIssue, ProfileDef } from '../../../types/schemas';

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
}) {
  const cardRef = useRef<HTMLDivElement>(null);
  const [measuredHeight, setMeasuredHeight] = useState(72);

  const { attributes, listeners, setNodeRef, transform, isDragging } = useDraggable({
    id: issue.identifier,
    data: { issue },
  });

  const style =
    transform && !isDragging ? { transform: CSS.Translate.toString(transform) } : undefined;

  // Measure card height in an effect so it happens outside render
  useEffect(() => {
    if (!isDragging && cardRef.current) {
      const h = cardRef.current.offsetHeight;
      startTransition(() => {
        setMeasuredHeight(h);
      });
    }
  });

  if (isDragging) {
    const h = measuredHeight;
    return (
      <div
        ref={setNodeRef}
        {...attributes}
        {...listeners}
        className={`border-theme-line-strong overflow-hidden rounded-lg border-2 border-dashed transition-all duration-300 ease-in-out ${
          shouldCollapse ? 'my-0 max-h-0 border-0 opacity-0' : 'opacity-100'
        }`}
        style={shouldCollapse ? undefined : { height: h }}
      />
    );
  }

  return (
    <div
      ref={(node) => {
        setNodeRef(node);
        cardRef.current = node;
      }}
      style={style}
      {...attributes}
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
      />
    </div>
  );
}
