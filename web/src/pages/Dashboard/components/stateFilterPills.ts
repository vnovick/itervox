import type { FilterPill } from '../../../components/itervox/FilterPills';

interface StateGroups {
  activeStates: readonly string[];
  backlogStates: readonly string[];
  completionState: string;
  terminalStates: readonly string[];
}

/**
 * The dashboard's state filter pills: All, Active, Backlog, the completion
 * state (e.g. "In Review") and Done. A completion state that is also
 * terminal, or is itself named "Done", folds into the Done pill instead of
 * adding a second "Done" label.
 */
export function buildStateFilterPills({
  activeStates,
  backlogStates,
  completionState,
  terminalStates,
}: StateGroups): FilterPill[] {
  const pills: FilterPill[] = [{ id: 'all', label: 'All Issues', states: [] }];
  if (activeStates.length > 0)
    pills.push({ id: 'active', label: 'Active', states: [...activeStates] });
  if (backlogStates.length > 0)
    pills.push({ id: 'backlog', label: 'Backlog', states: [...backlogStates] });
  const doneStates = [...terminalStates];
  const completion = completionState.toLowerCase();
  const completionIsDone =
    completion === 'done' || terminalStates.some((t) => t.toLowerCase() === completion);
  if (completionState && completionIsDone) {
    if (!doneStates.includes(completionState)) doneStates.push(completionState);
  } else if (completionState) {
    pills.push({ id: 'review', label: completionState, states: [completionState] });
  }
  if (doneStates.length > 0) pills.push({ id: 'done', label: 'Done', states: doneStates });
  return pills;
}
