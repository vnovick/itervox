import { useMemo, useState } from 'react';
import type { ProfileDef } from '../../types/schemas';
import { Card } from '../../components/ui/Card/Card';
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
      <Card variant="elevated" padding="none" className="overflow-hidden">
        <Card.Header className="bg-theme-panel-strong mb-0 flex items-center justify-between gap-4 px-5 py-4">
          <div>
            <h2 className="text-theme-text text-sm font-semibold">Agent Profiles</h2>
            <p className="text-theme-text-secondary mt-0.5 text-xs">
              Reusable worker presets for backend, model, prompt, and agent actions.
            </p>
          </div>
          <button
            onClick={openAddModal}
            className="bg-theme-accent rounded-[var(--radius-sm)] px-5 py-2 text-sm font-medium whitespace-nowrap text-white transition-colors hover:opacity-90"
          >
            Add Agent Profile
          </button>
        </Card.Header>

        <Card.Body className="min-w-0">
          {profileEntries.length > 0 ? (
            <div className="grid min-w-0 grid-cols-1 gap-3 xl:grid-cols-2">
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
            <div className="px-5 py-10 text-center text-sm">
              No profiles configured yet.{' '}
              <button onClick={openAddModal} className="text-theme-accent hover:underline">
                Add one
              </button>
            </div>
          )}
        </Card.Body>

        {suggestedToShow.length > 0 && (
          <Card.Footer className="mt-0 px-5 py-4">
            <p className="text-theme-muted mb-3 text-[11px] font-medium tracking-wider uppercase">
              Start From A Template
            </p>
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              {suggestedToShow.map((suggestion) => (
                <SuggestedProfileCard
                  key={suggestion.id}
                  suggestion={suggestion}
                  onUse={openTemplateModal}
                  supportedAgentActions={supportedAgentActions}
                />
              ))}
            </div>
          </Card.Footer>
        )}
      </Card>

      {status && <p className="text-theme-danger text-sm">{status.message}</p>}

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
