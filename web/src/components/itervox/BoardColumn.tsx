import { useDroppable } from '@dnd-kit/core';
import DraggableCard from './BoardColumn/DraggableCard';
import { columnSubtitle } from './BoardColumn/columnSubtitle';
import type { TrackerIssue } from '../../types/schemas';
import type { ProfileDef } from '../../types/schemas';
import {
  profileColor,
  profileInitials,
  UNASSIGNED_COLOR,
  type ProfileColor,
} from '../../utils/profileColors';
import { backendLabel, backendBadgeClass } from '../../pages/Settings/profiles/profileBadges';
import { commandToBackend, commandToModel, modelLabel } from '../../pages/Settings/profileCommands';

// ─── Rich agent header (used in Agent Queue view) ─────────────────────

function AgentColumnHeader({
  label,
  profileDef,
  color,
  issueCount,
  onHeaderClick,
}: {
  label: string;
  profileDef?: ProfileDef;
  color: ProfileColor;
  issueCount: number;
  onHeaderClick?: () => void;
}) {
  const initials = profileInitials(label);
  const inferredBackend = profileDef
    ? commandToBackend(profileDef.command, profileDef.backend)
    : 'claude';
  const model = profileDef ? commandToModel(profileDef.command) : '';
  const modelDisplay = model ? modelLabel(inferredBackend, model) : '';
  const instructionSnippet = profileDef?.instructions ?? profileDef?.prompt ?? '';
  const promptSnippet = instructionSnippet
    ? instructionSnippet.length > 100
      ? instructionSnippet.slice(0, 100) + '…'
      : instructionSnippet
    : null;

  return (
    <div
      className={`flex-shrink-0 ${onHeaderClick ? 'cursor-pointer' : ''}`}
      onClick={onHeaderClick}
      role={onHeaderClick ? 'button' : undefined}
      tabIndex={onHeaderClick ? 0 : undefined}
      onKeyDown={
        onHeaderClick
          ? (e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                onHeaderClick();
              }
            }
          : undefined
      }
    >
      {/* Colored top edge */}
      <div
        className="h-[3px] rounded-t-[var(--radius-md)]"
        style={{
          background: `linear-gradient(90deg, ${color.accent}, ${color.accent}44)`,
          opacity: 0.8,
        }}
      />

      <div className="px-3 pt-3 pb-2">
        <div className="flex items-start gap-2.5">
          {/* Avatar */}
          <div
            className="relative flex h-9 w-9 flex-shrink-0 items-center justify-center overflow-hidden rounded-[10px] text-xs font-bold text-white"
            style={{ background: color.gradient }}
          >
            <span className="relative z-10">{initials}</span>
            <div className="rounded-inherit absolute inset-0 bg-gradient-to-br from-white/15 to-transparent" />
          </div>

          {/* Meta */}
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-1.5">
              <h3 className="text-theme-text m-0 truncate text-xs font-semibold">{label}</h3>
              {profileDef && (
                <>
                  <span
                    className={`flex-shrink-0 rounded-full px-1.5 py-0.5 text-[9px] font-semibold tracking-wide uppercase ${backendBadgeClass(inferredBackend)}`}
                  >
                    {backendLabel(inferredBackend)}
                  </span>
                  {modelDisplay && (
                    <span className="text-theme-text-secondary bg-theme-bg-soft flex-shrink-0 rounded-full px-1.5 py-0.5 text-[9px] font-medium">
                      {modelDisplay}
                    </span>
                  )}
                </>
              )}
            </div>
            {promptSnippet && (
              <p className="text-theme-text-secondary mt-1 line-clamp-2 text-[11px] leading-snug">
                {promptSnippet}
              </p>
            )}
          </div>

          {/* Count badge */}
          <span
            className="flex min-w-[28px] flex-shrink-0 items-center justify-center rounded-full px-2.5 py-1 text-xs font-semibold"
            style={{
              background: color.bg,
              color: color.accent,
              border: `1px solid ${color.accent}22`,
            }}
          >
            {issueCount}
          </span>
        </div>
      </div>
    </div>
  );
}

// ─── Unassigned column header ─────────────────────────────────────────

function UnassignedColumnHeader({ issueCount }: { issueCount: number }) {
  return (
    <div className="flex-shrink-0">
      <div
        className="h-[3px] rounded-t-[var(--radius-md)]"
        style={{
          background: `linear-gradient(90deg, ${UNASSIGNED_COLOR.accent}, ${UNASSIGNED_COLOR.accent}44)`,
          opacity: 0.5,
        }}
      />
      <div className="px-3 pt-3 pb-2">
        <div className="flex items-start gap-2.5">
          <div
            className="text-theme-muted flex h-9 w-9 flex-shrink-0 items-center justify-center rounded-[10px] text-sm"
            style={{ background: UNASSIGNED_COLOR.gradient }}
          >
            ?
          </div>
          <div className="min-w-0 flex-1">
            <h3 className="text-theme-text m-0 truncate text-xs font-semibold">Unassigned</h3>
            <p className="text-theme-text-secondary mt-1 text-[11px] leading-snug">
              Drag to an agent to assign
            </p>
          </div>
          <span className="bg-theme-bg-elevated text-theme-text flex min-w-[28px] flex-shrink-0 items-center justify-center rounded-full px-2.5 py-1 text-xs font-semibold">
            {issueCount}
          </span>
        </div>
      </div>
    </div>
  );
}

// ─── Default state-based header (Board view) ──────────────────────────

function DefaultColumnHeader({
  label,
  issueCount,
  onInfoClick,
}: {
  state: string;
  label: string;
  issueCount: number;
  onInfoClick?: () => void;
}) {
  const subtitle = columnSubtitle(label);
  return (
    <div className="flex flex-shrink-0 items-start justify-between gap-3 px-3 py-3">
      <div className="min-w-0">
        <span className="flex items-center gap-1.5">
          <h3 className="text-theme-text m-0 truncate text-xs font-semibold tracking-wide uppercase">
            {label}
          </h3>
          {onInfoClick && (
            <button
              onClick={(e) => {
                e.stopPropagation();
                onInfoClick();
              }}
              className="text-theme-muted hover:text-theme-text flex h-4 w-4 flex-shrink-0 items-center justify-center rounded-full text-[10px] transition-colors"
              title={`About ${label}`}
              aria-label={`Info about ${label}`}
            >
              <svg
                width="12"
                height="12"
                viewBox="0 0 24 24"
                fill="none"
                stroke="currentColor"
                strokeWidth="2"
                strokeLinecap="round"
                strokeLinejoin="round"
              >
                <circle cx="12" cy="12" r="10" />
                <path d="M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3" />
                <line x1="12" y1="17" x2="12.01" y2="17" />
              </svg>
            </button>
          )}
        </span>
        {subtitle && (
          <p className="text-theme-text-secondary mt-1 text-[11px] leading-snug">{subtitle}</p>
        )}
      </div>
      <span className="bg-theme-bg-elevated text-theme-text flex min-w-[28px] flex-shrink-0 items-center justify-center rounded-full px-2.5 py-1 text-xs font-semibold">
        {issueCount}
      </span>
    </div>
  );
}

// ─── Main BoardColumn ─────────────────────────────────────────────────

interface ColumnProps {
  state: string;
  /** Display label — defaults to `state` if not provided */
  label?: string;
  issues: TrackerIssue[];
  isOver: boolean;
  /** Identifier of the card being dragged from this column (undefined if none) */
  draggingId?: string;
  /** True when the dragged card has moved to a different column */
  isCardOutside?: boolean;
  onSelect: (id: string) => void;
  availableProfiles?: string[];
  profileDefs?: Record<string, ProfileDef>;
  runningBackendByIdentifier?: Record<string, string>;
  /** T-6 surface: latest run kind ("worker" | "reviewer") per identifier. */
  runningKindByIdentifier?: Record<string, string>;
  /** T-6 surface: review-comment count per identifier. */
  commentCountByIdentifier?: Record<string, number>;
  /** Gap A: stale + age data for input_required entries, by identifier. */
  inputRequiredStaleByIdentifier?: Record<string, { stale: boolean; ageMinutes?: number }>;
  /** G: retry attempt number per identifier, when an issue is mid-retry. */
  retryAttemptByIdentifier?: Record<string, number>;
  /** G: snapshot's max_retries for the "↻ retry N/M" denominator. 0 = unlimited. */
  maxRetries?: number;
  /** outbox Task 4: identifiers with a pending write-ahead-outbox entry (snapshot.outboxSyncing). */
  syncingIdentifiers?: ReadonlySet<string>;
  defaultBackend?: string;
  onProfileChange?: (identifier: string, profile: string) => void;
  onDispatch?: (identifier: string) => void;
  /** Show (?) info button next to column header (Board view) */
  onInfoClick?: () => void;
  /** Click handler for the rich agent header (Agent Queue view) */
  onHeaderClick?: () => void;
  /** Whether this column represents the "unassigned" bucket */
  isUnassigned?: boolean;
  /** ProfileDef for this specific column's agent (Agent Queue view) */
  columnProfileDef?: ProfileDef;
}

export default function BoardColumn({
  state,
  label,
  issues,
  isOver,
  draggingId,
  isCardOutside,
  onSelect,
  availableProfiles,
  profileDefs,
  runningBackendByIdentifier,
  runningKindByIdentifier,
  commentCountByIdentifier,
  inputRequiredStaleByIdentifier,
  retryAttemptByIdentifier,
  maxRetries,
  defaultBackend,
  onProfileChange,
  onDispatch,
  onInfoClick,
  onHeaderClick,
  isUnassigned,
  columnProfileDef,
  syncingIdentifiers,
}: ColumnProps) {
  const { setNodeRef } = useDroppable({ id: state });

  // Determine which header to render:
  // - isUnassigned === true → UnassignedColumnHeader
  // - columnProfileDef or onHeaderClick → AgentColumnHeader (rich)
  // - default → DefaultColumnHeader (state-based)
  const isAgentView = isUnassigned !== undefined;
  const color = isAgentView && !isUnassigned ? profileColor(label ?? state) : null;

  return (
    <div
      ref={setNodeRef}
      className="border-theme-line bg-theme-bg-soft relative flex max-h-[85vh] w-[280px] min-w-[280px] flex-shrink-0 flex-col overflow-hidden rounded-[var(--radius-md)] border transition-colors duration-200"
      style={color && isOver ? { borderColor: `${color.accent}44` } : undefined}
    >
      {/* Dark overlay when dropping */}
      <div
        className={`pointer-events-none absolute inset-0 z-10 rounded-[var(--radius-md)] transition-opacity duration-150 ${
          isOver ? 'opacity-100' : 'opacity-0'
        }`}
        style={{ background: 'rgba(0,0,0,0.25)' }}
      />

      {/* Column header */}
      {isUnassigned ? (
        <UnassignedColumnHeader issueCount={issues.length} />
      ) : isAgentView && color ? (
        <AgentColumnHeader
          label={label ?? state}
          profileDef={columnProfileDef}
          color={color}
          issueCount={issues.length}
          onHeaderClick={onHeaderClick}
        />
      ) : (
        <DefaultColumnHeader
          state={state}
          label={label ?? state}
          issueCount={issues.length}
          onInfoClick={onInfoClick}
        />
      )}

      {/* Issue cards */}
      <div className="flex-1 space-y-2 overflow-y-auto px-3 pb-3">
        {issues.map((issue) => (
          <DraggableCard
            key={issue.identifier}
            issue={issue}
            isBeingDragged={draggingId === issue.identifier}
            shouldCollapse={draggingId === issue.identifier && (isCardOutside ?? false)}
            onSelect={onSelect}
            availableProfiles={availableProfiles}
            profileDefs={profileDefs}
            runningBackendByIdentifier={runningBackendByIdentifier}
            runningKindByIdentifier={runningKindByIdentifier}
            commentCountByIdentifier={commentCountByIdentifier}
            inputRequiredStaleByIdentifier={inputRequiredStaleByIdentifier}
            retryAttemptByIdentifier={retryAttemptByIdentifier}
            maxRetries={maxRetries}
            syncingIdentifiers={syncingIdentifiers}
            defaultBackend={defaultBackend}
            onProfileChange={onProfileChange}
            onDispatch={onDispatch}
          />
        ))}
      </div>
    </div>
  );
}
