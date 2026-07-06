import PageMeta from '../../components/common/PageMeta';
import { GeneralCard } from './GeneralCard';
import { TrackerStatesCard } from './TrackerStatesCard';
import { WorkspaceCard } from './WorkspaceCard';
import { ProjectFilterCard } from './ProjectFilterCard';
import { SSHHostsCard } from './SSHHostsCard';
import { SkillsCard } from './SkillsCard';
import { RetriesCard } from './RetriesCard';
import { ConfirmButton } from '../../components/ui/button/ConfirmButton';
import { useClearAllLogs, useClearAllWorkspaces } from '../../queries/issues';
import { useSettingsPageData } from './useSettingsPageData';

export default function Settings() {
  const {
    activeStates,
    terminalStates,
    completionState,
    autoClearWorkspace,
    autoReview,
    inlineInput,
    trackerKind,
    activeProjectFilter,
    maxRetries,
    failedState,
    maxSwitchesPerIssuePerWindow,
    switchWindowHours,
    trackerStateOptions,
    updateTrackerStates,
    setAutoClearWorkspace,
    setProjectFilter,
    setInlineInput,
    setMaxRetries,
    setFailedState,
    setMaxSwitchesPerIssuePerWindow,
    setSwitchWindowHours,
  } = useSettingsPageData();
  const clearAllLogs = useClearAllLogs();
  const clearAllWorkspaces = useClearAllWorkspaces();

  return (
    <>
      <PageMeta
        title="Itervox | Settings"
        description="Itervox settings — profiles, tracker states, and workspace"
      />
      <div className="w-full max-w-5xl space-y-8">
        <div>
          <h1 className="text-theme-text text-2xl font-bold tracking-tight">Settings</h1>
          <p className="text-theme-muted mt-1 text-sm">
            Configure tracker, workspace, connectivity, and maintenance behaviour. Agent profiles
            and automations now live on their own dedicated pages. All settings are also
            hot-reloaded from{' '}
            <code className="bg-theme-bg-soft text-theme-accent rounded px-1.5 py-0.5 font-mono text-xs">
              WORKFLOW.md
            </code>
            .
          </p>
        </div>

        <section aria-labelledby="section-general">
          <h2 id="section-general" className="mb-3 text-xs font-semibold tracking-widest uppercase">
            General
          </h2>
          <GeneralCard inlineInput={inlineInput} onSetInlineInput={setInlineInput} />
        </section>

        <section aria-labelledby="section-tracker">
          <h2 id="section-tracker" className="mb-3 text-xs font-semibold tracking-widest uppercase">
            Tracker States
          </h2>
          <div className="space-y-4">
            <TrackerStatesCard
              initialActiveStates={activeStates}
              initialTerminalStates={terminalStates}
              initialCompletionState={completionState}
              onSave={updateTrackerStates}
            />
            {trackerKind === 'linear' && (
              <ProjectFilterCard
                activeFilter={activeProjectFilter}
                onSetFilter={setProjectFilter}
              />
            )}
          </div>
        </section>

        <section aria-labelledby="section-workspace">
          <h2
            id="section-workspace"
            className="mb-3 text-xs font-semibold tracking-widest uppercase"
          >
            Workspace
          </h2>
          <WorkspaceCard
            autoClearWorkspace={autoClearWorkspace}
            autoReviewEnabled={autoReview}
            onToggle={setAutoClearWorkspace}
          />
        </section>

        <section aria-labelledby="section-retries">
          <h2 id="section-retries" className="mb-3 text-xs font-semibold tracking-widest uppercase">
            Retries
          </h2>
          <RetriesCard
            maxRetries={maxRetries}
            failedState={failedState}
            trackerStateOptions={trackerStateOptions}
            completionState={completionState}
            maxSwitchesPerIssuePerWindow={maxSwitchesPerIssuePerWindow}
            switchWindowHours={switchWindowHours}
            onSetMaxRetries={setMaxRetries}
            onSetFailedState={setFailedState}
            onSetMaxSwitchesPerIssuePerWindow={setMaxSwitchesPerIssuePerWindow}
            onSetSwitchWindowHours={setSwitchWindowHours}
          />
        </section>

        <section aria-labelledby="section-ssh-hosts">
          <h2
            id="section-ssh-hosts"
            className="mb-3 text-xs font-semibold tracking-widest uppercase"
          >
            SSH Hosts
          </h2>
          <SSHHostsCard />
        </section>

        <section aria-labelledby="section-skills">
          <h2 id="section-skills" className="mb-3 text-xs font-semibold tracking-widest uppercase">
            Skills Inventory
          </h2>
          <SkillsCard />
        </section>

        <section aria-labelledby="section-logs">
          <h2 id="section-logs" className="mb-3 text-xs font-semibold tracking-widest uppercase">
            Logs
          </h2>
          <div className="border-theme-line bg-theme-panel space-y-3 rounded-lg border p-4">
            <div className="flex items-center justify-between gap-4">
              <div>
                <p className="text-theme-text text-sm font-medium">Clear all logs</p>
                <p className="text-theme-muted mt-0.5 text-xs">
                  Deletes in-memory and on-disk log buffers for all issues.
                </p>
              </div>
              <ConfirmButton
                label="Clear all logs"
                confirmLabel="Yes, clear"
                pendingLabel="Clearing…"
                isPending={clearAllLogs.isPending}
                onConfirm={() => {
                  clearAllLogs.mutate(undefined);
                }}
              />
            </div>

            <div className="border-theme-line border-t" />

            <div className="flex items-center justify-between gap-4">
              <div>
                <p className="text-theme-text text-sm font-medium">Reset all workspaces</p>
                <p className="text-theme-muted mt-0.5 text-xs">
                  Deletes all cloned workspace directories under workspace.root. Does not affect
                  logs or tracker state.
                </p>
              </div>
              <ConfirmButton
                label="Reset workspaces"
                confirmLabel="Yes, reset"
                pendingLabel="Resetting…"
                isPending={clearAllWorkspaces.isPending}
                onConfirm={() => {
                  clearAllWorkspaces.mutate(undefined);
                }}
              />
            </div>
          </div>
        </section>
      </div>
    </>
  );
}
