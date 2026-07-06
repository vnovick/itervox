import { useState, useMemo, useCallback } from 'react';
import {
  DndContext,
  DragOverlay,
  KeyboardSensor,
  PointerSensor,
  TouchSensor,
  useSensor,
  useSensors,
  type DragEndEvent,
  type DragStartEvent,
  type DragOverEvent,
} from '@dnd-kit/core';
import IssueCard from './IssueCard';
import BoardColumn from './BoardColumn';
import { AgentInfoModal } from './AgentInfoModal';
import type { TrackerIssue, ProfileDef } from '../../types/schemas';

const UNASSIGNED = '__unassigned__';

interface AgentQueueViewProps {
  issues: TrackerIssue[];
  backlogStates: string[];
  availableProfiles: string[];
  profileDefs?: Record<string, ProfileDef>;
  availableModels?: Record<string, { id: string; label: string }[]>;
  supportedAgentActions?: readonly string[];
  onProfileChange: (identifier: string, profile: string) => void;
  onSelect: (id: string) => void;
  onEditProfile?: (name: string, def: ProfileDef) => Promise<void>;
}

export default function AgentQueueView({
  issues,
  backlogStates,
  availableProfiles,
  profileDefs,
  availableModels,
  supportedAgentActions,
  onProfileChange,
  onSelect,
  onEditProfile,
}: AgentQueueViewProps) {
  const [activeIssue, setActiveIssue] = useState<TrackerIssue | null>(null);
  const [overId, setOverId] = useState<string | null>(null);
  const [infoProfile, setInfoProfile] = useState<string | null>(null);

  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 5 } }),
    useSensor(TouchSensor, { activationConstraint: { delay: 250, tolerance: 5 } }),
    useSensor(KeyboardSensor),
  );

  const backlogSet = useMemo(() => new Set(backlogStates), [backlogStates]);

  const columns = useMemo(() => {
    const unassigned: TrackerIssue[] = [];
    const byProfile = new Map<string, TrackerIssue[]>(availableProfiles.map((p) => [p, []]));
    for (const issue of issues) {
      if (!backlogSet.has(issue.state)) continue;
      const p = issue.agentProfile;
      if (p && byProfile.has(p)) {
        (byProfile.get(p) as TrackerIssue[]).push(issue);
      } else {
        unassigned.push(issue);
      }
    }
    return [
      { id: UNASSIGNED, label: 'Unassigned', issues: unassigned },
      ...availableProfiles.map((p) => ({ id: p, label: p, issues: byProfile.get(p) ?? [] })),
    ];
  }, [issues, backlogSet, availableProfiles]);

  const onDragStart = useCallback((e: DragStartEvent) => {
    const data = e.active.data.current as { issue?: TrackerIssue } | undefined;
    if (data?.issue) setActiveIssue(data.issue);
  }, []);

  const onDragOver = useCallback((e: DragOverEvent) => {
    setOverId(e.over ? String(e.over.id) : null);
  }, []);

  const onDragEnd = useCallback(
    (e: DragEndEvent) => {
      setActiveIssue(null);
      setOverId(null);
      if (!e.over) return;
      const droppedOn = String(e.over.id);
      const newProfile = droppedOn === UNASSIGNED ? '' : droppedOn;
      const currentProfile =
        (e.active.data.current as { issue?: TrackerIssue } | undefined)?.issue?.agentProfile ?? '';
      if (newProfile !== currentProfile) {
        onProfileChange(String(e.active.id), newProfile);
      }
    },
    [onProfileChange],
  );

  const totalBacklog = columns.reduce((sum, col) => sum + col.issues.length, 0);

  if (totalBacklog === 0) {
    return (
      <div className="border-theme-line bg-theme-bg-elevated text-theme-muted rounded-[var(--radius-md)] border px-6 py-12 text-center text-sm">
        No backlog issues to route
      </div>
    );
  }

  const activeSourceCol = activeIssue
    ? columns.find((col) => col.issues.some((i) => i.identifier === activeIssue.identifier))?.id
    : undefined;

  const infoDef = infoProfile ? profileDefs?.[infoProfile] : undefined;

  return (
    <>
      <DndContext
        sensors={sensors}
        onDragStart={onDragStart}
        onDragOver={onDragOver}
        onDragEnd={onDragEnd}
      >
        <div className="flex min-h-[200px] gap-3 overflow-x-auto pb-2">
          {columns.map((col) => {
            const isUnassigned = col.id === UNASSIGNED;
            return (
              <BoardColumn
                key={col.id}
                state={col.id}
                label={col.label}
                issues={col.issues}
                isOver={overId === col.id}
                draggingId={activeSourceCol === col.id ? activeIssue?.identifier : undefined}
                isCardOutside={activeSourceCol === col.id && overId !== null && overId !== col.id}
                onSelect={onSelect}
                isUnassigned={isUnassigned}
                columnProfileDef={!isUnassigned ? profileDefs?.[col.id] : undefined}
                onHeaderClick={
                  !isUnassigned
                    ? () => {
                        setInfoProfile(col.id);
                      }
                    : undefined
                }
              />
            );
          })}
        </div>
        <DragOverlay dropAnimation={null}>
          {activeIssue && (
            <div className="w-[280px]">
              <IssueCard issue={activeIssue} isDragging onSelect={() => {}} />
            </div>
          )}
        </DragOverlay>
      </DndContext>

      <AgentInfoModal
        profileName={infoProfile}
        profileDef={infoDef}
        onClose={() => {
          setInfoProfile(null);
        }}
        onSave={onEditProfile}
        availableModels={availableModels}
        supportedAgentActions={supportedAgentActions}
      />
    </>
  );
}
