import { useMemo } from 'react';
import { useShallow } from 'zustand/react/shallow';
import { useItervoxStore } from '../../store/itervoxStore';
import { useSettingsActions } from '../../hooks/useSettingsActions';
import { useIssues } from '../../queries/issues';
import {
  EMPTY_AUTOMATIONS,
  EMPTY_PROFILE_DEFS,
  EMPTY_PROFILES,
  EMPTY_STATES,
} from '../../utils/constants';

export function useSettingsPageData() {
  const {
    activeStates,
    terminalStates,
    completionState,
    backlogStates,
    autoClearWorkspace,
    inlineInput,
    maxRetries,
    failedState,
    maxSwitchesPerIssuePerWindow,
    switchWindowHours,
  } = useItervoxStore(
    useShallow((s) => ({
      activeStates: s.snapshot?.activeStates ?? EMPTY_STATES,
      terminalStates: s.snapshot?.terminalStates ?? EMPTY_STATES,
      completionState: s.snapshot?.completionState ?? '',
      backlogStates: s.snapshot?.backlogStates ?? EMPTY_STATES,
      autoClearWorkspace: s.snapshot?.autoClearWorkspace ?? false,
      inlineInput: s.snapshot?.inlineInput ?? false,
      // G: server-side default is 5 (matches Go cfg.Agent.MaxRetries default).
      maxRetries: s.snapshot?.maxRetries ?? 5,
      failedState: s.snapshot?.failedState ?? '',
      maxSwitchesPerIssuePerWindow: s.snapshot?.maxSwitchesPerIssuePerWindow ?? 2,
      switchWindowHours: s.snapshot?.switchWindowHours ?? 6,
    })),
  );
  const profileDefs = useItervoxStore((s) => s.snapshot?.profileDefs ?? EMPTY_PROFILE_DEFS);
  const availableModels = useItervoxStore((s) => s.snapshot?.availableModels);
  const supportedAgentActions = useItervoxStore((s) => s.snapshot?.supportedAgentActions);
  const availableProfiles = useItervoxStore((s) => s.snapshot?.availableProfiles ?? EMPTY_PROFILES);
  const automations = useItervoxStore((s) => s.snapshot?.automations ?? EMPTY_AUTOMATIONS);
  const reviewerProfile = useItervoxStore((s) => s.snapshot?.reviewerProfile ?? '');
  const autoReview = useItervoxStore((s) => s.snapshot?.autoReview ?? false);
  const trackerKind = useItervoxStore((s) => s.snapshot?.trackerKind);
  const activeProjectFilter = useItervoxStore((s) => s.snapshot?.activeProjectFilter);

  const actions = useSettingsActions();
  const issuesQuery = useIssues();

  const trackerStateOptions = useMemo(() => {
    const observedStates = issuesQuery.data?.map((issue) => issue.state) ?? EMPTY_STATES;
    return Array.from(
      new Set(
        [
          ...backlogStates,
          ...activeStates,
          ...terminalStates,
          completionState,
          ...observedStates,
        ].filter(Boolean),
      ),
    ).sort((a, b) => a.localeCompare(b));
  }, [backlogStates, activeStates, terminalStates, completionState, issuesQuery.data]);

  const automationLabelOptions = useMemo(
    () =>
      Array.from(
        new Set(
          (issuesQuery.data ?? []).flatMap((issue) => issue.labels ?? EMPTY_STATES).filter(Boolean),
        ),
      ).sort((a, b) => a.localeCompare(b)),
    [issuesQuery.data],
  );

  const automationProfileOptions = useMemo(() => {
    const names = new Set(availableProfiles);
    for (const automation of automations) {
      if (automation.profile && automation.profile in profileDefs) {
        names.add(automation.profile);
      }
    }
    return Array.from(names).sort((a, b) => a.localeCompare(b));
  }, [automations, availableProfiles, profileDefs]);

  const reviewerProfileOptions = useMemo(() => {
    const names = new Set(availableProfiles);
    if (reviewerProfile && reviewerProfile in profileDefs) {
      names.add(reviewerProfile);
    }
    return Array.from(names).sort((a, b) => a.localeCompare(b));
  }, [availableProfiles, profileDefs, reviewerProfile]);

  return {
    activeStates,
    terminalStates,
    completionState,
    backlogStates,
    autoClearWorkspace,
    inlineInput,
    maxRetries,
    failedState,
    maxSwitchesPerIssuePerWindow,
    switchWindowHours,
    profileDefs,
    availableModels,
    supportedAgentActions,
    availableProfiles,
    automations,
    reviewerProfile,
    autoReview,
    trackerKind,
    activeProjectFilter,
    trackerStateOptions,
    automationLabelOptions,
    automationProfileOptions,
    reviewerProfileOptions,
    issuesQuery,
    ...actions,
  };
}
