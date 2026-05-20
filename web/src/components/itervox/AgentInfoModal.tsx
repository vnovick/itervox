import { memo, useState, useEffect, startTransition } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { Modal } from '../ui/modal';
import type { ProfileDef } from '../../types/schemas';
import { proseClass } from '../../utils/format';
import { ProfileEditorFields } from '../../pages/Settings/profiles/ProfileEditorFields';
import { backendLabel, backendBadgeClass } from '../../pages/Settings/profiles/profileBadges';
import {
  agentActionOptionsFor,
  normalizeAllowedActions,
  applyBackendSelection,
  applyModelSelection,
  commandToBackend,
  commandToModel,
  draftFromProfileDef,
  inferBackendFromCommand,
  modelLabel,
  normalizeCommandForSave,
  type AllowedAgentAction,
  type SupportedBackend,
} from '../../pages/Settings/profileCommands';
import { profileColor, profileInitials } from '../../utils/profileColors';

interface AgentInfoModalProps {
  profileName: string | null;
  profileDef?: ProfileDef;
  onClose: () => void;
  onSave?: (name: string, def: ProfileDef) => Promise<void>;
  availableModels?: Record<string, { id: string; label: string }[]>;
  supportedAgentActions?: readonly string[];
}

export const AgentInfoModal = memo(function AgentInfoModal({
  profileName,
  profileDef,
  onClose,
  onSave,
  availableModels,
  supportedAgentActions,
}: AgentInfoModalProps) {
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);

  // Form state — mirrors ProfileRow approach
  const initialDraft = profileDef
    ? draftFromProfileDef(profileDef)
    : {
        backend: 'claude' as SupportedBackend,
        model: '',
        command: '',
        prompt: '',
        allowedActions: [] as AllowedAgentAction[],
        createIssueState: '',
      };
  const [backend, setBackend] = useState<SupportedBackend>(initialDraft.backend);
  const [model, setModel] = useState(initialDraft.model);
  const [command, setCommand] = useState(initialDraft.command);
  const [prompt, setPrompt] = useState(initialDraft.prompt);
  const [allowedActions, setAllowedActions] = useState<AllowedAgentAction[]>(
    initialDraft.allowedActions,
  );
  const [createIssueState, setCreateIssueState] = useState(initialDraft.createIssueState);

  // Reset form when profile changes or modal opens
  useEffect(() => {
    startTransition(() => {
      if (profileDef) {
        const draft = draftFromProfileDef(profileDef);
        setBackend(draft.backend);
        setModel(draft.model);
        setCommand(draft.command);
        setPrompt(draft.prompt);
        setAllowedActions(draft.allowedActions);
        setCreateIssueState(draft.createIssueState);
      }
      setEditing(false);
      setSaving(false);
    });
  }, [profileDef, profileName]);

  const handleCancel = () => {
    if (profileDef) {
      const draft = draftFromProfileDef(profileDef);
      setBackend(draft.backend);
      setModel(draft.model);
      setCommand(draft.command);
      setPrompt(draft.prompt);
      setAllowedActions(draft.allowedActions);
      setCreateIssueState(draft.createIssueState);
    }
    setEditing(false);
  };

  const handleSave = async () => {
    if (!profileName || !onSave) return;
    setSaving(true);
    await onSave(profileName, {
      command: normalizeCommandForSave(command, backend),
      backend,
      prompt: prompt.trim() || undefined,
      enabled: profileDef?.enabled ?? true,
      allowedActions: allowedActions.length > 0 ? allowedActions : undefined,
      createIssueState: allowedActions.includes('create_issue')
        ? createIssueState.trim() || undefined
        : undefined,
    });
    setSaving(false);
    setEditing(false);
  };

  const color = profileName ? profileColor(profileName) : null;
  const initials = profileName ? profileInitials(profileName) : '';
  const inferredBackend = profileDef
    ? commandToBackend(profileDef.command, profileDef.backend)
    : 'claude';
  const profileModel = profileDef ? commandToModel(profileDef.command) : '';
  const modelDisplay = profileModel ? modelLabel(inferredBackend, profileModel) : '';
  const actionLabels = agentActionOptionsFor(supportedAgentActions)
    .filter((option) => (profileDef?.allowedActions ?? []).includes(option.id))
    .map((option) => option.label);

  return (
    <Modal
      isOpen={profileName !== null}
      onClose={onClose}
      showCloseButton
      className="max-h-[85vh] max-w-lg overflow-y-auto"
    >
      {profileName && color && (
        <div data-testid="agent-info-content">
          {/* Colored top edge */}
          <div
            className="h-1 rounded-t-[var(--radius-lg)]"
            style={{ background: `linear-gradient(90deg, ${color.accent}, ${color.accent}66)` }}
          />

          <div className="p-6">
            {/* Agent identity header */}
            <div className="flex items-start gap-3">
              <div
                className="flex h-11 w-11 flex-shrink-0 items-center justify-center rounded-xl text-base font-bold text-white"
                style={{ background: color.gradient }}
              >
                <span className="relative z-10">{initials}</span>
              </div>
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <h2 className="text-theme-text text-base font-semibold">{profileName}</h2>
                  <span
                    className={`rounded-full px-2 py-0.5 text-[10px] font-medium ${backendBadgeClass(inferredBackend)}`}
                  >
                    {backendLabel(inferredBackend)}
                  </span>
                  {modelDisplay && (
                    <span className="text-theme-text-secondary bg-theme-bg-soft rounded-full px-2 py-0.5 text-[10px] font-medium">
                      {modelDisplay}
                    </span>
                  )}
                </div>
                {!editing && profileDef?.prompt && (
                  <div
                    className={`border-theme-line bg-theme-panel-strong mt-3 max-h-[50vh] overflow-y-auto rounded-[var(--radius-sm)] border p-4 ${proseClass}`}
                  >
                    <ReactMarkdown remarkPlugins={[remarkGfm]}>{profileDef.prompt}</ReactMarkdown>
                  </div>
                )}
                {!editing && actionLabels.length > 0 && (
                  <div className="mt-3 flex flex-wrap gap-1.5">
                    {actionLabels.map((label) => (
                      <span
                        key={label}
                        className="bg-theme-bg-soft text-theme-text-secondary rounded-full px-2 py-0.5 text-[10px]"
                      >
                        {label}
                      </span>
                    ))}
                  </div>
                )}
              </div>
            </div>

            {/* View mode */}
            {!editing && (
              <div className="mt-5">
                {!profileDef?.prompt && (
                  <p className="text-theme-muted text-sm">No prompt configured for this profile.</p>
                )}
                {onSave && (
                  <button
                    onClick={() => {
                      setEditing(true);
                    }}
                    className="border-theme-line text-theme-text-secondary mt-4 rounded-[var(--radius-sm)] border px-4 py-2 text-sm font-medium transition-colors hover:opacity-80"
                  >
                    Edit Profile
                  </button>
                )}
              </div>
            )}

            {/* Edit mode */}
            {editing && (
              <div className="mt-5 space-y-3">
                <ProfileEditorFields
                  backend={backend}
                  model={model}
                  command={command}
                  prompt={prompt}
                  allowedActions={allowedActions}
                  supportedAgentActions={supportedAgentActions}
                  createIssueState={createIssueState}
                  onBackendChange={(value) => {
                    const next = applyBackendSelection(command, backend, value);
                    setBackend(value);
                    setModel(next.model);
                    setCommand(next.command);
                  }}
                  onModelChange={(value) => {
                    setModel(value);
                    setCommand(applyModelSelection(command, backend, value));
                  }}
                  onCommandChange={(value) => {
                    setCommand(value);
                    setModel(commandToModel(value));
                    const inferred = inferBackendFromCommand(value);
                    if (inferred) setBackend(inferred);
                  }}
                  onPromptChange={setPrompt}
                  onAllowedActionsChange={(value) => {
                    const normalized = normalizeAllowedActions(value, supportedAgentActions);
                    setAllowedActions(normalized);
                    if (!normalized.includes('create_issue')) {
                      setCreateIssueState('');
                    }
                  }}
                  onCreateIssueStateChange={setCreateIssueState}
                  dynamicModels={availableModels}
                />
                <div className="flex items-center gap-2 pt-2">
                  <button
                    onClick={() => {
                      void handleSave();
                    }}
                    disabled={saving || !command.trim()}
                    className="bg-theme-accent rounded-[var(--radius-sm)] px-4 py-2 text-sm font-medium text-white transition-colors disabled:opacity-50"
                  >
                    {saving ? 'Saving…' : 'Save'}
                  </button>
                  <button
                    onClick={handleCancel}
                    className="border-theme-line text-theme-text-secondary rounded-[var(--radius-sm)] border px-4 py-2 text-sm font-medium transition-colors hover:opacity-80"
                  >
                    Cancel
                  </button>
                </div>
              </div>
            )}
          </div>
        </div>
      )}
    </Modal>
  );
});
