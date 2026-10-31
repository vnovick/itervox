import PageMeta from '../../components/common/PageMeta';
import { Link } from 'react-router';
import { ModelsCard } from '../Settings/ModelsCard';
import { ProfilesCard } from '../Settings/ProfilesCard';
import { ReviewerCard } from '../Settings/ReviewerCard';
import { useSettingsPageData } from '../Settings/useSettingsPageData';

export default function Agents() {
  const {
    profileDefs,
    availableModels,
    supportedAgentActions,
    trackerStateOptions,
    reviewerProfile,
    autoReview,
    autoClearWorkspace,
    reviewerProfileOptions,
    upsertProfile,
    deleteProfile,
    setReviewerConfig,
  } = useSettingsPageData();

  return (
    <>
      <PageMeta
        title="Itervox | Agents"
        description="Itervox agent management — profiles, reviewer behavior, and execution capacity"
      />
      <div className="flex w-full max-w-none flex-col gap-4">
        <div className="px-1.5 pt-1">
          <h1 className="page-title">Agents</h1>
          <p className="text-theme-text-secondary text-sm">
            Manage agent profiles, reviewer behavior, and execution capacity. Profiles remain synced
            with <code className="font-mono text-[13px]">WORKFLOW.md</code>.
          </p>
        </div>

        <ProfilesCard
          headingId="section-profiles"
          profileDefs={profileDefs}
          onUpsert={upsertProfile}
          onDelete={deleteProfile}
          availableModels={availableModels}
          supportedAgentActions={supportedAgentActions}
          trackerStates={trackerStateOptions}
        />

        <div className="grid gap-4 lg:grid-cols-2">
          <section
            aria-labelledby="section-reviewer"
            className="glass-panel flex min-w-0 flex-col gap-3 rounded-[26px] px-5 py-[18px]"
          >
            <h2 id="section-reviewer" className="text-theme-text text-base font-semibold">
              Code Review Agent
            </h2>
            <ReviewerCard
              reviewerProfile={reviewerProfile}
              autoReview={autoReview}
              autoClearWorkspace={autoClearWorkspace}
              availableProfiles={reviewerProfileOptions}
              onSave={setReviewerConfig}
            />
            {/* CORE-093 — Capacity moved to Settings, next to Retries. */}
            <p className="text-theme-muted text-xs">
              Max concurrent agents is set under{' '}
              <Link
                to="/settings#section-capacity"
                className="text-theme-accent-text underline underline-offset-2"
              >
                Settings → Capacity
              </Link>
              .
            </p>
          </section>

          <div className="min-w-0">
            <ModelsCard headingId="section-models" availableModels={availableModels || {}} />
          </div>
        </div>
      </div>
    </>
  );
}
