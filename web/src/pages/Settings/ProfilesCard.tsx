import { useMemo, useState } from 'react';
import type { ProfileDef } from '../../types/schemas';
import { Button } from '../../components/ui/button';
import { ProfileFormModal } from './profiles/ProfileFormModal';
import { ProfileRow } from './profiles/ProfileRow';
import { SuggestedProfileCard } from './profiles/SuggestedProfileCard';
import { SUGGESTED_PROFILES, type SuggestedProfile } from './profiles/suggestedProfiles';
import {
  emptyProfileValues,
  profileValuesFromDef,
  profileValuesFromSuggestion,
  type ProfileFormValues,
} from './profiles/profileForm';

interface ProfilesCardProps {
  profileDefs: Record<string, ProfileDef>;
  onUpsert: (
    name: string,
    command: string,
    backend?: string,
    prompt?: string,
    soul?: string,
    instructions?: string,
    soulFile?: string,
    instructionsFile?: string,
    enabled?: boolean,
    allowedActions?: string[],
    createIssueState?: string,
    originalName?: string,
  ) => Promise<boolean>;
  onDelete: (name: string) => Promise<boolean>;
  availableModels?: Record<string, { id: string; label: string }[]>;
  supportedAgentActions?: readonly string[];
  trackerStates?: readonly string[];
  /** Id for the section heading, so a page can link or label it. */
  headingId?: string;
  /** Opens this profile's editor once it is listed (the palette's Edit). */
  focusProfileName?: string;
}

type ProfileModalState = {
  mode: 'add' | 'edit';
  title: string;
  subtitle?: string;
  submitLabel: string;
  initialValues: ProfileFormValues;
};

type ProfileStatus = {
  kind: 'error';
  message: string;
};

export function ProfilesCard({
  profileDefs,
  onUpsert,
  onDelete,
  availableModels,
  supportedAgentActions,
  trackerStates,
  headingId = 'profiles-card-heading',
  focusProfileName,
}: ProfilesCardProps) {
  const [modalState, setModalState] = useState<ProfileModalState | null>(null);
  const [status, setStatus] = useState<ProfileStatus | null>(null);

  const profileEntries = useMemo(
    () => Object.entries(profileDefs).sort(([a], [b]) => a.localeCompare(b)),
    [profileDefs],
  );
  const suggestedToShow = useMemo(
    () => SUGGESTED_PROFILES.filter((s) => !(s.id in profileDefs)),
    [profileDefs],
  );

  const openAddModal = () => {
    setModalState({
      mode: 'add',
      title: 'Add Agent Profile',
      subtitle:
        'Create a reusable agent profile with backend, prompt, and daemon-backed action permissions.',
      submitLabel: 'Create Profile',
      initialValues: emptyProfileValues(),
    });
  };

  const openEditModal = (name: string, def: ProfileDef) => {
    setModalState({
      mode: 'edit',
      title: `Edit "${name}"`,
      subtitle: 'Update the profile configuration used by issue workers and helper agents.',
      submitLabel: 'Save Changes',
      initialValues: profileValuesFromDef(name, def),
    });
  };

  // Open the editor for the focused name once per name change, adjusting
  // state during render like AutomationsCard's focusAutomationId: only marked
  // handled once the profile is listed, so a link that arrives before the
  // snapshot still opens it.
  const [lastFocused, setLastFocused] = useState<string | undefined>(undefined);
  if (focusProfileName && focusProfileName !== lastFocused && focusProfileName in profileDefs) {
    setLastFocused(focusProfileName);
    openEditModal(focusProfileName, profileDefs[focusProfileName]);
  }

  const openTemplateModal = (suggestion: SuggestedProfile) => {
    setModalState({
      mode: 'add',
      title: `Use "${suggestion.label}" Template`,
      subtitle: suggestion.description,
      submitLabel: 'Create Profile',
      initialValues: profileValuesFromSuggestion(suggestion),
    });
  };

  const handleDelete = async (name: string) => {
    setStatus(null);
    const ok = await onDelete(name);
    if (!ok) {
      setStatus({
        kind: 'error',
        message: `Failed to delete profile "${name}". Check the server logs.`,
      });
    }
  };

  const handleToggleEnabled = async (name: string, def: ProfileDef, enabled: boolean) => {
    await onUpsert(
      name,
      def.command,
      def.backend,
      def.prompt,
      def.soul,
      def.instructions,
      def.soulFile,
      def.instructionsFile,
      enabled,
      def.allowedActions,
      def.createIssueState,
      name,
    );
  };

  const hasProfileNameConflict = (name: string, originalName?: string) => {
    const normalizedName = name.trim().toLowerCase();
    const normalizedOriginal = originalName?.trim().toLowerCase() ?? '';
    return profileEntries.some(([existingName]) => {
      const normalizedExisting = existingName.trim().toLowerCase();
      return normalizedExisting === normalizedName && normalizedExisting !== normalizedOriginal;
    });
  };

  return (
    <>
      <section
        aria-labelledby={headingId}
        className="glass-panel flex flex-col gap-2.5 rounded-[26px] px-5 py-[18px]"
      >
        <div className="flex flex-wrap items-center gap-2.5">
          <div className="mr-auto">
            <h2 id={headingId} className="text-theme-text text-base font-semibold">
              Agent Profiles
            </h2>
            <p className="text-theme-text-secondary text-[13px]">
              Reusable worker presets for backend, model, prompt, and agent actions.
            </p>
          </div>
          <Button variant="primary" size="lg" pill onClick={openAddModal}>
            Add Agent Profile
          </Button>
        </div>

        {profileEntries.length > 0 ? (
          <div className="flex min-w-0 flex-col gap-2.5">
            {profileEntries.map(([name, def]) => (
              <ProfileRow
                key={name}
                name={name}
                def={def}
                onEdit={() => {
                  openEditModal(name, def);
                }}
                onToggleEnabled={handleToggleEnabled}
                onDelete={handleDelete}
                supportedAgentActions={supportedAgentActions}
              />
            ))}
          </div>
        ) : (
          <div className="glass-card text-theme-text-secondary px-5 py-8 text-center text-sm">
            No profiles configured yet.{' '}
            <button onClick={openAddModal} className="text-theme-accent-text hover:underline">
              Add one
            </button>
          </div>
        )}

        {suggestedToShow.length > 0 && (
          <div className="mt-2">
            <h3 className="text-theme-text-secondary mb-2 text-[13px] font-semibold">
              Start From A Template
            </h3>
            <div className="grid grid-cols-[repeat(auto-fill,minmax(180px,1fr))] gap-2">
              {suggestedToShow.map((suggestion) => (
                <SuggestedProfileCard
                  key={suggestion.id}
                  suggestion={suggestion}
                  onUse={openTemplateModal}
                  supportedAgentActions={supportedAgentActions}
                />
              ))}
            </div>
          </div>
        )}
      </section>

      {status && <p className="text-theme-danger-text text-sm">{status.message}</p>}

      {modalState && (
        <ProfileFormModal
          key={`${modalState.mode}:${modalState.initialValues.name}`}
          isOpen
          mode={modalState.mode}
          title={modalState.title}
          subtitle={modalState.subtitle}
          submitLabel={modalState.submitLabel}
          initialValues={modalState.initialValues}
          availableModels={availableModels}
          supportedAgentActions={supportedAgentActions}
          trackerStates={trackerStates}
          onClose={() => {
            setModalState(null);
          }}
          onSubmit={async (name, def, originalName) => {
            setStatus(null);
            if (hasProfileNameConflict(name, originalName)) {
              setStatus({ kind: 'error', message: 'Profile names must be unique.' });
              return false;
            }
            return onUpsert(
              name,
              def.command,
              def.backend,
              def.prompt,
              def.soul,
              def.instructions,
              def.soulFile,
              def.instructionsFile,
              def.enabled,
              def.allowedActions,
              def.createIssueState,
              originalName,
            );
          }}
        />
      )}
    </>
  );
}
