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
import { EditorCard } from './EditorCard';
import { SchemaSettingsCard } from './schema/SchemaSettingsCard';
import { useSettingsPageData } from './useSettingsPageData';
import { RequireScope } from '../../components/itervox/RequireScope';
import { useCallerScope } from '../../hooks/useCallerScope';
import { scopeAllows } from '../../auth/scope';
import { AccessCard } from './AccessCard';
import { MaintenanceSection } from './MaintenanceSection';

// A settings panel, one titled section inside it, and the hairline that
// separates sub-cards within a section.
const PANEL = 'glass-panel min-w-0 space-y-3 px-5 py-[18px]';
const SECTION = 'space-y-3 [&+&]:pt-3';
const TITLE = 'section-title';
const DIVIDED = 'border-theme-line border-t pt-4';

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
      <div className="page-container">
        <div className="page-header">
          <h1 className="page-title">Settings</h1>
          <p className="text-theme-text-secondary mt-1 max-w-3xl text-sm">
            Configure tracker, workspace, connectivity, and maintenance behaviour. Agent profiles
            and automations now live on their own dedicated pages. All settings are also
            hot-reloaded from{' '}
            <code className="text-theme-accent-text font-mono text-[13px]">WORKFLOW.md</code>.
          </p>
        </div>

        {!isAdmin && (
          <p role="note" data-testid="settings-scope-notice" className="banner-warn">
            Your token has the {scope} scope, so settings are hidden here. Changing settings needs
            an admin token.
          </p>
        )}

        <div className="grid [grid-template-columns:repeat(auto-fill,minmax(min(100%,22rem),1fr))] items-start gap-4">
          <div className={PANEL}>
            {isAdmin && (
              <section aria-labelledby="section-general" className={SECTION}>
                <h2 id="section-general" className={TITLE}>
                  General
                </h2>
                <GeneralCard inlineInput={inlineInput} onSetInlineInput={setInlineInput} />
              </section>
            )}
            <section aria-labelledby="section-notifications" className={SECTION}>
              <h2 id="section-notifications" className={TITLE}>
                Browser notifications
              </h2>
              <BrowserNotificationsCard />
            </section>
            <section aria-labelledby="section-editor" className={SECTION}>
              <h2 id="section-editor" className={TITLE}>
                Editor
              </h2>
              <EditorCard />
            </section>
          </div>

          {isAdmin && (
            <>
              {/* CORE-093 — Capacity (max concurrent agents) sits next to Retries:
              both decide how much work runs at once and how failures are
              handled. It moved here from the Agents page. */}
              <div className={PANEL}>
                <section aria-labelledby="section-retries" className={SECTION}>
                  <h2 id="section-retries" className={TITLE}>
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
                <section aria-labelledby="section-capacity" className={SECTION}>
                  <h2 id="section-capacity" className={TITLE}>
                    Capacity
                  </h2>
                  <CapacityCard />
                </section>
              </div>

              <section aria-labelledby="section-tracker" className={PANEL}>
                <h2 id="section-tracker" className={TITLE}>
                  Tracker States
                </h2>
                <TrackerStatesCard
                  initialActiveStates={activeStates}
                  initialTerminalStates={terminalStates}
                  initialCompletionState={completionState}
                  onSave={updateTrackerStates}
                />
                {filterSupported ? (
                  <div className={DIVIDED}>
                    <TrackerFilterCard onSave={setTrackerFilter} />
                  </div>
                ) : (
                  trackerKind === 'linear' && (
                    <div className={DIVIDED}>
                      <ProjectFilterCard
                        activeFilter={activeProjectFilter}
                        onSetFilter={setProjectFilter}
                      />
                    </div>
                  )
                )}
              </section>

              <section aria-labelledby="section-access" className={PANEL}>
                <h2 id="section-access" className={TITLE}>
                  Access
                </h2>
                <AccessCard />
              </section>

              <section aria-labelledby="section-ssh-hosts" className={PANEL}>
                <h2 id="section-ssh-hosts" className={TITLE}>
                  SSH Hosts
                </h2>
                <SSHHostsCard />
              </section>

              <div className={PANEL}>
                <section aria-labelledby="section-workspace" className={SECTION}>
                  <h2 id="section-workspace" className={TITLE}>
                    Workspace
                  </h2>
                  <WorkspaceCard
                    autoClearWorkspace={autoClearWorkspace}
                    autoReviewEnabled={autoReview}
                    onToggle={setAutoClearWorkspace}
                  />
                </section>
                <section aria-labelledby="section-dependencies" className={SECTION}>
                  <h2 id="section-dependencies" className={TITLE}>
                    Dependencies
                  </h2>
                  <DependenciesCard mode={depsAnalysisMode} onSetMode={setDepsAnalysisMode} />
                </section>
                <section aria-labelledby="section-rate-limits" className={SECTION}>
                  <h2 id="section-rate-limits" className={TITLE}>
                    Rate limits &amp; failover
                  </h2>
                  <RateLimitsFailoverCard
                    maxSwitchesPerIssuePerWindow={maxSwitchesPerIssuePerWindow}
                    switchWindowHours={switchWindowHours}
                    onSetMaxSwitchesPerIssuePerWindow={setMaxSwitchesPerIssuePerWindow}
                    onSetSwitchWindowHours={setSwitchWindowHours}
                  />
                </section>
              </div>
            </>
          )}
        </div>

        {/* #158 — every layered setting, rendered from the settings schema. */}
        {isAdmin && (
          <section aria-labelledby="section-all-settings" className={PANEL}>
            {/* A compact toolbar (layer tabs, filter, section links) over
                sections that start collapsed and open on demand. */}
            <SchemaSettingsCard
              heading={
                <h2 id="section-all-settings" className={`${TITLE} mr-2`}>
                  All settings
                </h2>
              }
              help={
                <p className="field-help">
                  Every agent, automations, dependencies, hooks, hub, polling, server, tracker and
                  workspace setting, from the settings schema. Pick the layer a save writes: your
                  user file, the project file (committed) or the project-local file (gitignored).
                  The project and local files win over the same key in WORKFLOW.md (whose front
                  matter is deprecated for them), and WORKFLOW.md wins over your user file.
                </p>
              }
            />
          </section>
        )}

        <RequireScope scope="operator">
          <MaintenanceSection isAdmin={isAdmin} />
        </RequireScope>

        {/* #200 — the inventory is analysis, not a setting. */}
        <p className="text-theme-text-secondary px-1 text-sm">
          Skills inventory moved to{' '}
          <Link
            to="/repo#capabilities"
            className="text-theme-accent-text underline underline-offset-2"
          >
            Repo Analysis
          </Link>
          .
        </p>
      </div>
    </>
  );
}
