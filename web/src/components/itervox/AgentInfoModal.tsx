import { memo, useState, useEffect, startTransition } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import { Modal } from '../ui/modal';
import type { ProfileDef } from '../../types/schemas';
import { proseClass } from '../../utils/format';
import { ProfileEditorFields } from '../../pages/Settings/profiles/ProfileEditorFields';
import { backendLabel } from '../../pages/Settings/profiles/profileBadges';
import { Button } from '../ui/button';
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
        soul: '',
        instructions: '',
        soulFile: '',
        instructionsFile: '',
        allowedActions: [] as AllowedAgentAction[],
        createIssueState: '',
      };
  const [backend, setBackend] = useState<SupportedBackend>(initialDraft.backend);
  const [model, setModel] = useState(initialDraft.model);
  const [command, setCommand] = useState(initialDraft.command);
  const [prompt, setPrompt] = useState(initialDraft.prompt);
  const [soul, setSoul] = useState(initialDraft.soul);
  const [instructions, setInstructions] = useState(initialDraft.instructions);
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
        setSoul(draft.soul);
        setInstructions(draft.instructions);
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
      setSoul(draft.soul);
      setInstructions(draft.instructions);
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
      prompt: instructions.trim() || prompt.trim() || undefined,
      soul,
      instructions,
      soulFile: profileDef?.soulFile,
      instructionsFile: profileDef?.instructionsFile,
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
  const displaySoul = profileDef?.soul?.trim() ?? '';
  const displayInstructions = (profileDef?.instructions ?? profileDef?.prompt ?? '').trim();
  const hasProfileText = displaySoul !== '' || displayInstructions !== '';

  // Default tab follows content availability so single-document profiles land
  // on the populated tab. Reset during render when the profile identity
  // changes (React's "adjust state on prop change" pattern) — avoids the
  // cascading re-render that a setState-in-effect would cause.
  const [activeTab, setActiveTab] = useState<'soul' | 'instructions'>(
    displaySoul ? 'soul' : 'instructions',
  );
  const [prevProfileName, setPrevProfileName] = useState(profileName);
  if (profileName !== prevProfileName) {
    setPrevProfileName(profileName);
    setActiveTab(displaySoul ? 'soul' : 'instructions');
  }

  const tabClass = 'min-h-9 text-[15px]';

  return (
    <Modal
      isOpen={profileName !== null}
      onClose={onClose}
      showCloseButton
      className="flex h-[85vh] w-[90vw] max-w-[1300px] flex-col overflow-hidden"
      ariaLabel={profileName ? `Agent profile ${profileName}` : 'Agent profile'}
    >
      {profileName && color && (
        <div className="flex h-[85vh] flex-col" data-testid="agent-info-content">
          {/* Header (fixed); the right padding clears the close button. */}
          <div className="flex-shrink-0 px-7 pt-6 pr-20 pb-4">
            <div className="flex items-start gap-3">
              <div
                aria-hidden="true"
                className="flex h-9 w-9 flex-shrink-0 items-center justify-center rounded-[var(--radius-sm)] text-sm font-bold text-white"
                style={{ background: color.gradient }}
              >
                {initials}
              </div>
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2.5">
                  <h2 className="text-theme-text text-[22px] font-bold tracking-tight">
                    {profileName}
                  </h2>
                  <span
                    data-testid="agent-info-backend"
                    className="pill pill-accent px-2 py-0 text-xs font-semibold"
                  >
                    {backendLabel(inferredBackend)}
                  </span>
                  <span
                    data-testid="agent-info-model"
                    className="pill pill-neutral px-2 py-0 text-xs"
                  >
                    {modelDisplay || 'Default model'}
                  </span>
                </div>
                {!editing && actionLabels.length > 0 && (
                  <div className="mt-3 flex flex-wrap gap-1.5">
                    {actionLabels.map((label) => (
                      <span key={label} className="pill pill-neutral px-2 py-0 text-xs">
                        {label}
                      </span>
                    ))}
                  </div>
                )}
              </div>
              {!editing && onSave && (
                <Button
                  variant="glass"
                  size="lg"
                  onClick={() => {
                    setEditing(true);
                  }}
                >
                  Edit Profile
                </Button>
              )}
            </div>
          </div>

          {/* Body (fills remaining height, scrolls internally) */}
          <div className="flex flex-1 flex-col overflow-hidden px-7 pb-6">
            {/* View mode: tabs over SOUL / Instructions */}
            {!editing && hasProfileText && (
              <>
                <div
                  className="segmented flex-shrink-0 self-start"
                  role="tablist"
                  aria-label="Profile documents"
                >
                  {displaySoul && (
                    <button
                      type="button"
                      role="tab"
                      aria-selected={activeTab === 'soul'}
                      onClick={() => {
                        setActiveTab('soul');
                      }}
                      className={tabClass}
                    >
                      SOUL.md
                    </button>
                  )}
                  {displayInstructions && (
                    <button
                      type="button"
                      role="tab"
                      aria-selected={activeTab === 'instructions'}
                      onClick={() => {
                        setActiveTab('instructions');
                      }}
                      className={tabClass}
                    >
                      INSTRUCTIONS.md
                    </button>
                  )}
                </div>
                <div
                  role="tabpanel"
                  className={`glass-card mt-4 flex-1 overflow-y-auto rounded-[20px] px-6 py-5 ${proseClass}`}
                >
                  {activeTab === 'soul' && displaySoul && (
                    <ReactMarkdown remarkPlugins={[remarkGfm]}>{displaySoul}</ReactMarkdown>
                  )}
                  {activeTab === 'instructions' && displayInstructions && (
                    <ReactMarkdown remarkPlugins={[remarkGfm]}>{displayInstructions}</ReactMarkdown>
                  )}
                </div>
              </>
            )}

            {/* View mode: empty state */}
            {!editing && !hasProfileText && (
              <p className="glass-card text-theme-muted px-6 py-5 text-sm">
                No profile files configured for this profile.
              </p>
            )}

            {/* Edit mode */}
            {editing && (
              <div className="flex-1 space-y-3 overflow-y-auto">
                <ProfileEditorFields
                  profileName={profileName}
                  backend={backend}
                  model={model}
                  command={command}
                  prompt={prompt}
                  soul={soul}
                  instructions={instructions}
                  soulFile={profileDef?.soulFile}
                  instructionsFile={profileDef?.instructionsFile}
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
                  onPromptChange={(value) => {
                    setPrompt(value);
                    setInstructions(value);
                  }}
                  onSoulChange={setSoul}
                  onInstructionsChange={(value) => {
                    setInstructions(value);
                    setPrompt(value);
                  }}
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
                <div className="flex items-center justify-end gap-2 pt-2">
                  <Button variant="glass" size="lg" onClick={handleCancel}>
                    Cancel
                  </Button>
                  <Button
                    variant="primary"
                    size="lg"
                    onClick={() => {
                      void handleSave();
                    }}
                    disabled={saving || !command.trim()}
                  >
                    {saving ? 'Saving…' : 'Save'}
                  </Button>
                </div>
              </div>
            )}
          </div>
        </div>
      )}
    </Modal>
  );
});
