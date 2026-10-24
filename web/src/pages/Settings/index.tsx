import { Link } from 'react-router';
import PageMeta from '../../components/common/PageMeta';
import { GeneralCard } from './GeneralCard';
import { TrackerStatesCard } from './TrackerStatesCard';
import { WorkspaceCard } from './WorkspaceCard';
import { DependenciesCard } from './DependenciesCard';
import { ProjectFilterCard } from './ProjectFilterCard';
import { TrackerFilterCard } from './trackerFilter/TrackerFilterCard';
import { SSHHostsCard } from './SSHHostsCard';
import { RetriesCard } from './RetriesCard';
import { CapacityCard } from './CapacityCard';
import { RateLimitsFailoverCard } from './RateLimitsFailoverCard';
import { BrowserNotificationsCard } from './BrowserNotificationsCard';
import { SchemaSettingsCard } from './schema/SchemaSettingsCard';
import { useSettingsPageData } from './useSettingsPageData';
import { RequireScope } from '../../components/itervox/RequireScope';
import { useCallerScope } from '../../hooks/useCallerScope';
import { scopeAllows } from '../../auth/scope';
import { AccessCard } from './AccessCard';
import { MaintenanceSection } from './MaintenanceSection';

export default function Settings() {
  const {
    activeStates,
    terminalStates,
    completionState,
    autoClearWorkspace,
    depsAnalysisMode,
    autoReview,
    inlineInput,
    trackerKind,
    activeProjectFilter,
    filterSupported,
    maxRetries,
    failedState,
    maxSwitchesPerIssuePerWindow,
    switchWindowHours,
    trackerStateOptions,
    updateTrackerStates,
    setAutoClearWorkspace,
    setDepsAnalysisMode,
    setProjectFilter,
    setTrackerFilter,
    setInlineInput,
    setMaxRetries,
    setFailedState,
    setMaxSwitchesPerIssuePerWindow,
    setSwitchWindowHours,
  } = useSettingsPageData();
  // #214 — settings, tokens and workspace resets are admin; clearing logs is
  // operator. The daemon enforces this; the page hides what the scope
  // cannot use.
  const scope = useCallerScope();
  const isAdmin = scopeAllows(scope, 'admin');

  return (
    <>
      <PageMeta
        title="Itervox | Settings"
        description="Itervox settings — tracker states, workspace, retries, capacity, rate limits and failover, SSH hosts"
      />
      <div className="w-full max-w-5xl space-y-8">
        <div>
          <h1 className="text-theme-text text-2xl font-bold tracking-tight">Settings</h1>
          <p className="text-theme-muted mt-1 text-sm">
            Configure tracker, workspace, connectivity, and maintenance behaviour. Agent profiles
            and automations now live on their own dedicated pages. All settings are also
            hot-reloaded from{' '}
            <code className="bg-theme-bg-soft text-theme-accent-text rounded px-1.5 py-0.5 font-mono text-xs">
              WORKFLOW.md
            </code>
            .
          </p>
        </div>

        {!isAdmin && (
          <p
            role="note"
            data-testid="settings-scope-notice"
            className="border-theme-line bg-theme-bg-soft text-theme-text-secondary rounded-[var(--radius-sm)] border px-4 py-3 text-sm"
          >
            Your token has the {scope} scope, so settings are hidden here. Changing settings needs
            an admin token.
          </p>
        )}

        {isAdmin && (
          <section aria-labelledby="section-general">
            <h2
              id="section-general"
              className="mb-3 text-xs font-semibold tracking-widest uppercase"
            >
              General
            </h2>
            <GeneralCard inlineInput={inlineInput} onSetInlineInput={setInlineInput} />
          </section>
        )}

        <section aria-labelledby="section-notifications">
          <h2
            id="section-notifications"
            className="mb-3 text-xs font-semibold tracking-widest uppercase"
          >
            Browser notifications
          </h2>
          <BrowserNotificationsCard />
        </section>

        {isAdmin && (
          <>
            <section aria-labelledby="section-access">
              <h2
                id="section-access"
                className="mb-3 text-xs font-semibold tracking-widest uppercase"
              >
                Access
              </h2>
              <AccessCard />
            </section>

            <section aria-labelledby="section-tracker">
              <h2
                id="section-tracker"
                className="mb-3 text-xs font-semibold tracking-widest uppercase"
              >
                Tracker States
              </h2>
              <div className="space-y-4">
                <TrackerStatesCard
                  initialActiveStates={activeStates}
                  initialTerminalStates={terminalStates}
                  initialCompletionState={completionState}
                  onSave={updateTrackerStates}
                />
                {filterSupported ? (
                  <TrackerFilterCard onSave={setTrackerFilter} />
                ) : (
                  trackerKind === 'linear' && (
                    <ProjectFilterCard
                      activeFilter={activeProjectFilter}
                      onSetFilter={setProjectFilter}
                    />
                  )
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

            <section aria-labelledby="section-dependencies">
              <h2
                id="section-dependencies"
                className="mb-3 text-xs font-semibold tracking-widest uppercase"
              >
                Dependencies
              </h2>
              <DependenciesCard mode={depsAnalysisMode} onSetMode={setDepsAnalysisMode} />
            </section>

            <section aria-labelledby="section-retries">
              <h2
                id="section-retries"
                className="mb-3 text-xs font-semibold tracking-widest uppercase"
              >
                Retries
              </h2>
              <RetriesCard
                maxRetries={maxRetries}
                failedState={failedState}
                trackerStateOptions={trackerStateOptions}
                completionState={completionState}
                onSetMaxRetries={setMaxRetries}
                onSetFailedState={setFailedState}
              />
            </section>

            {/* CORE-093 — Capacity (max concurrent agents) sits next to Retries:
            both decide how much work runs at once and how failures are
            handled. It moved here from the Agents page. */}
            <section aria-labelledby="section-capacity">
              <h2
                id="section-capacity"
                className="mb-3 text-xs font-semibold tracking-widest uppercase"
              >
                Capacity
              </h2>
              <CapacityCard />
            </section>

            <section aria-labelledby="section-rate-limits">
              <h2
                id="section-rate-limits"
                className="mb-3 text-xs font-semibold tracking-widest uppercase"
              >
                Rate limits &amp; failover
              </h2>
              <RateLimitsFailoverCard
                maxSwitchesPerIssuePerWindow={maxSwitchesPerIssuePerWindow}
                switchWindowHours={switchWindowHours}
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

            {/* #158 — every layered setting, rendered from the settings schema. */}
            <section aria-labelledby="section-all-settings">
              <h2
                id="section-all-settings"
                className="mb-1 text-xs font-semibold tracking-widest uppercase"
              >
                All settings
              </h2>
              <p className="text-theme-muted mb-3 text-xs">
                Every agent, automations, dependencies, hooks, hub, polling, server, tracker and
                workspace setting, from the settings schema. Pick the layer a save writes: your user
                file, the project file (committed) or the project-local file (gitignored). Each wins
                over the same key in WORKFLOW.md, whose front matter is deprecated for them.
              </p>
              <SchemaSettingsCard />
            </section>
          </>
        )}

        {/* #200 — the inventory is analysis, not a setting. */}
        <p className="text-theme-muted text-sm">
          Skills inventory moved to{' '}
          <Link to="/repo#capabilities" className="text-theme-accent hover:underline">
            Repo Analysis
          </Link>
          .
        </p>

        <RequireScope scope="operator">
          <MaintenanceSection isAdmin={isAdmin} />
        </RequireScope>
      </div>
    </>
  );
}
