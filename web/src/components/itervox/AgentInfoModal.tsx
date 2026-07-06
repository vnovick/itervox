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

  return (
    <Modal
      isOpen={profileName !== null}
      onClose={onClose}
      showCloseButton
      className="flex h-[85vh] w-[90vw] max-w-[1400px] flex-col overflow-hidden"
    >
      {profileName && color && (
        <div className="flex h-full flex-col" data-testid="agent-info-content">
          {/* Colored top edge */}
          <div
            className="h-1 flex-shrink-0 rounded-t-[var(--radius-lg)]"
            style={{ background: `linear-gradient(90deg, ${color.accent}, ${color.accent}66)` }}
          />

          {/* Header (fixed) */}
          <div className="flex-shrink-0 px-6 pt-6 pb-4">
            <div className="flex items-start gap-3">
              <div
                className="flex h-11 w-11 flex-shrink-0 items-center justify-center rounded-xl text-base font-bold text-white"
                style={{ background: color.gradient }}
              >
                <span className="relative z-10">{initials}</span>
              </div>
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
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
          </div>

          {/* Body (fills remaining height, scrolls internally) */}
          <div className="flex flex-1 flex-col overflow-hidden px-6 pb-6">
            {/* View mode: tabs over SOUL / Instructions */}
            {!editing && hasProfileText && (
              <>
                <div
                  className="border-theme-line flex flex-shrink-0 gap-1 border-b"
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
                      className={`-mb-px border-b-2 px-4 py-2 text-[12px] font-semibold tracking-wide uppercase transition-colors ${
                        activeTab === 'soul'
                          ? 'border-theme-accent text-theme-text'
                          : 'text-theme-muted hover:text-theme-text-secondary border-transparent'
                      }`}
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
                      className={`-mb-px border-b-2 px-4 py-2 text-[12px] font-semibold tracking-wide uppercase transition-colors ${
                        activeTab === 'instructions'
                          ? 'border-theme-accent text-theme-text'
                          : 'text-theme-muted hover:text-theme-text-secondary border-transparent'
                      }`}
                    >
                      INSTRUCTIONS.md
                    </button>
                  )}
                </div>
                <div
                  role="tabpanel"
                  className={`border-theme-line bg-theme-panel-strong mt-3 flex-1 overflow-y-auto rounded-[var(--radius-sm)] border p-5 ${proseClass}`}
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

            {/* View mode: empty state + edit button */}
            {!editing && !hasProfileText && (
              <p className="text-theme-muted text-sm">
                No profile files configured for this profile.
              </p>
            )}
            {!editing && onSave && (
              <div className="flex-shrink-0 pt-4">
                <button
                  onClick={() => {
                    setEditing(true);
                  }}
                  className="border-theme-line text-theme-text-secondary rounded-[var(--radius-sm)] border px-4 py-2 text-sm font-medium transition-colors hover:opacity-80"
                >
                  Edit Profile
                </button>
              </div>
            )}

            {/* Edit mode */}
            {editing && (
              <div className="flex-1 space-y-3 overflow-y-auto">
                <ProfileEditorFields
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
