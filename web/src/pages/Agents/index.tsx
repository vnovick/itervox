import PageMeta from '../../components/common/PageMeta';
import { CapacityCard } from '../Settings/CapacityCard';
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
      <div className="w-full max-w-none space-y-8">
        <div>
          <h1 className="text-theme-text text-2xl font-bold tracking-tight">Agents</h1>
          <p className="text-theme-muted mt-1 text-sm">
            Manage agent profiles, reviewer behavior, and execution capacity. Profiles remain synced
            with{' '}
            <code className="bg-theme-bg-soft text-theme-accent mx-1 rounded px-1.5 py-0.5 font-mono text-xs">
              WORKFLOW.md
            </code>
            .
          </p>
        </div>

        <div className="grid gap-8 xl:grid-cols-[minmax(0,1.15fr)_minmax(0,0.9fr)]">
          <section aria-labelledby="section-profiles" className="min-w-0">
            <h2
              id="section-profiles"
              className="mb-3 text-xs font-semibold tracking-widest uppercase"
            >
              Profiles
            </h2>
            <ProfilesCard
              profileDefs={profileDefs}
              onUpsert={upsertProfile}
              onDelete={deleteProfile}
              availableModels={availableModels}
              supportedAgentActions={supportedAgentActions}
              trackerStates={trackerStateOptions}
            />
          </section>

          <div className="min-w-0 space-y-8">
            <section aria-labelledby="section-reviewer">
              <h2
                id="section-reviewer"
                className="mb-3 text-xs font-semibold tracking-widest uppercase"
              >
                Code Review Agent
              </h2>
              <ReviewerCard
                reviewerProfile={reviewerProfile}
                autoReview={autoReview}
                autoClearWorkspace={autoClearWorkspace}
                availableProfiles={reviewerProfileOptions}
                onSave={setReviewerConfig}
              />
            </section>

            <section aria-labelledby="section-capacity">
              <h2
                id="section-capacity"
                className="mb-3 text-xs font-semibold tracking-widest uppercase"
              >
                Capacity
              </h2>
              <CapacityCard />
            </section>

            <section aria-labelledby="section-models">
              <h2
                id="section-models"
                className="mb-3 text-xs font-semibold tracking-widest uppercase"
              >
                Models
              </h2>
              <ModelsCard availableModels={availableModels || {}} />
            </section>
          </div>
        </div>
      </div>
    </>
  );
}
