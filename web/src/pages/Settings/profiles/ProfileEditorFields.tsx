import { useState, type ReactNode } from 'react';
import { AGENT_BACKENDS, BACKEND_BINARIES, BACKEND_LABELS } from '../../../lib/backends';
import {
  agentActionOptionsFor,
  normalizeAllowedActions,
  isSimpleBackendCommand,
  modelsForBackend,
  normalizeBackend,
  type AllowedAgentAction,
  type ModelOption,
  type SupportedBackend,
} from '../profileCommands';
import { checkboxCls, fieldLabelCls, helperTextCls, inputCls, selectCls } from '../formStyles';
import { MarkdownPromptEditor } from './MarkdownPromptEditor';
import { ProfileReferencePicker } from './ProfileReferencePicker';
import { appendReference, guessProfileBackend } from './backendInventory';

function ModelInput({
  id,
  backend,
  value,
  onChange,
  dynamicModels,
}: {
  id?: string;
  backend: SupportedBackend;
  value: string;
  onChange: (v: string) => void;
  dynamicModels?: Record<string, ModelOption[]>;
}) {
  const models = modelsForBackend(backend, dynamicModels);
  const isKnownModel = !value || models.some((m) => m.id === value);
  return (
    <>
      <select
        id={id}
        value={isKnownModel ? value : '__custom__'}
        onChange={(e) => {
          const v = e.target.value;
          onChange(v === '__custom__' ? '' : v);
        }}
        className={selectCls}
      >
        <option value="">Default model</option>
        {models.map((m) => (
          <option key={m.id} value={m.id}>
            {m.id} — {m.label}
          </option>
        ))}
        <option value="__custom__">Custom model ID…</option>
      </select>
      {!isKnownModel && (
        <input
          value={value}
          onChange={(e) => {
            onChange(e.target.value);
          }}
          placeholder="Enter custom model ID"
          className={`${inputCls} mt-1 font-mono text-xs`}
        />
      )}
    </>
  );
}

function BackendSelect({
  id,
  value,
  onChange,
}: {
  id?: string;
  value: SupportedBackend;
  onChange: (value: SupportedBackend) => void;
}) {
  return (
    <select
      id={id}
      value={value}
      onChange={(e) => {
        onChange(normalizeBackend(e.target.value));
      }}
      className={selectCls}
    >
      {AGENT_BACKENDS.map((b) => (
        <option key={b} value={b}>
          {BACKEND_LABELS[b]}
        </option>
      ))}
    </select>
  );
}

function commandPlaceholder(backend: SupportedBackend): string {
  if (backend === 'custom') return './my-agent.sh — a command speaking the itervox agent protocol';
  const bin = BACKEND_BINARIES[backend];
  return `${bin}, /path/to/${bin}, or a wrapper command`;
}

const PROMPT_VARIABLES = [
  ['{{ issue.identifier }}', 'Issue ID (e.g. ENG-42)'],
  ['{{ issue.title }}', 'Issue title'],
  ['{{ issue.description }}', 'Issue body'],
  ['{{ issue.url }}', 'Issue URL'],
  ['{{ issue.branch_name }}', 'Git branch name'],
  ['{{ issue.labels }}', 'Labels array'],
  ['{{ issue.priority }}', 'Priority level'],
  ['{{ attempt }}', 'Retry attempt number'],
] as const;

interface ProfileEditorFieldsProps {
  backend: SupportedBackend;
  model: string;
  command: string;
  prompt: string;
  soul?: string;
  instructions?: string;
  soulFile?: string;
  instructionsFile?: string;
  allowedActions: AllowedAgentAction[];
  supportedAgentActions?: readonly string[];
  createIssueState: string;
  trackerStates?: readonly string[];
  createIssueStateError?: string;
  onBackendChange: (value: SupportedBackend) => void;
  onModelChange: (value: string) => void;
  onCommandChange: (value: string) => void;
  onPromptChange: (value: string) => void;
  onSoulChange?: (value: string) => void;
  onInstructionsChange?: (value: string) => void;
  onAllowedActionsChange: (value: AllowedAgentAction[]) => void;
  onCreateIssueStateChange: (value: string) => void;
  dynamicModels?: Record<string, ModelOption[]>;
  /** Profile name, used in reference-check messages (#123). */
  profileName?: string;
  /** A field rendered first in the backend / model row (the profile name in the form). */
  leadingField?: ReactNode;
  /** Rendered right under the backend / model row (the form's Active toggle). */
  afterFirstRow?: ReactNode;
}

export function ProfileEditorFields({
  backend,
  model,
  command,
  prompt,
  soul,
  instructions,
  soulFile,
  instructionsFile,
  allowedActions,
  supportedAgentActions,
  createIssueState,
  trackerStates,
  createIssueStateError,
  onBackendChange,
  onModelChange,
  onCommandChange,
  onPromptChange,
  onSoulChange,
  onInstructionsChange,
  onAllowedActionsChange,
  onCreateIssueStateChange,
  dynamicModels,
  profileName = '',
  leadingField,
  afterFirstRow,
}: ProfileEditorFieldsProps) {
  // A custom backend is always a hand-written command (the itervox agent
  // protocol), so its command field is always shown.
  const isCustomCommand = backend === 'custom' || !isSimpleBackendCommand(command, backend);
  const canCreateIssue = allowedActions.includes('create_issue');
  const actionOptions = agentActionOptionsFor(supportedAgentActions);
  const [advancedOpen, setAdvancedOpen] = useState(isCustomCommand);
  const [prevIsCustomCommand, setPrevIsCustomCommand] = useState(isCustomCommand);
  const isFileBacked =
    !!soulFile || !!instructionsFile || soul !== undefined || instructions !== undefined;
  // The prompt editors check references and filter suggestions for this
  // draft's backend (#123); the daemon resolves it from command + backend.
  const profileContext = { name: profileName, command, backend };
  const instructionsValue = instructions ?? prompt;
  const onInstructions = onInstructionsChange ?? onPromptChange;

  if (isCustomCommand !== prevIsCustomCommand) {
    setPrevIsCustomCommand(isCustomCommand);
    if (isCustomCommand) {
      setAdvancedOpen(true);
    }
  }

  return (
    <div className="space-y-4">
      <div className={`grid gap-3 ${leadingField ? 'md:grid-cols-3' : 'md:grid-cols-2'}`}>
        {leadingField}
        <div>
          <label htmlFor="profile-backend-select" className={fieldLabelCls}>
            Backend
          </label>
          <BackendSelect id="profile-backend-select" value={backend} onChange={onBackendChange} />
        </div>
        <div>
          <label htmlFor="profile-model-input" className={fieldLabelCls}>
            Model
          </label>
          <ModelInput
            id="profile-model-input"
            backend={backend}
            value={model}
            onChange={onModelChange}
            dynamicModels={dynamicModels}
          />
        </div>
      </div>

      {afterFirstRow}

      <div className="grid gap-4 xl:grid-cols-[minmax(0,1.15fr)_minmax(0,0.85fr)]">
        <fieldset className="field-group space-y-2">
          <legend>Daemon Actions</legend>
          <p className="field-help">
            Daemon-backed actions available to this profile. Invoke them with{' '}
            <span className="font-mono">itervox action ...</span>, not with{' '}
            <span className="font-mono">{'{{ }}'}</span>. Local workers only in v1.
          </p>
          <div className="space-y-1.5">
            {actionOptions.map((option) => {
              const checked = allowedActions.includes(option.id);
              return (
                <label key={option.id} className="flex items-start gap-2 text-[13px]">
                  <input
                    type="checkbox"
                    checked={checked}
                    onChange={(e) => {
                      const next = e.target.checked
                        ? normalizeAllowedActions(
                            [...allowedActions, option.id],
                            supportedAgentActions,
                          )
                        : normalizeAllowedActions(
                            allowedActions.filter((action) => action !== option.id),
                            supportedAgentActions,
                          );
                      onAllowedActionsChange(next);
                    }}
                    className={checkboxCls}
                  />
                  <span className="min-w-0">
                    <span className="text-theme-text block">{option.label}</span>
                    <span className="text-theme-muted block text-xs">{option.description}</span>
                  </span>
                </label>
              );
            })}
          </div>
          {canCreateIssue && (
            <div className="space-y-2">
              <label htmlFor="profile-create-issue-state" className={fieldLabelCls}>
                Follow-up issue state
              </label>
              <input
                id="profile-create-issue-state"
                list="profile-create-issue-state-options"
                value={createIssueState}
                onChange={(e) => {
                  onCreateIssueStateChange(e.target.value);
                }}
                placeholder="Todo"
                className={`${inputCls} font-mono text-xs`}
              />
              {!!trackerStates?.length && (
                <datalist id="profile-create-issue-state-options">
                  {trackerStates.map((state) => (
                    <option key={state} value={state} />
                  ))}
                </datalist>
              )}
              <p className={helperTextCls}>
                Newly created follow-up issues will open in this tracker column/state.
              </p>
              {createIssueStateError && (
                <p role="alert" className="text-theme-danger-text text-xs">
                  {createIssueStateError}
                </p>
              )}
            </div>
          )}
        </fieldset>

        <div className="subtle-fill text-theme-text-secondary space-y-1.5 self-start rounded-[var(--radius-sm)] px-3 py-2.5 text-xs">
          <div>
            <p className="text-theme-text font-semibold">Prompt variables</p>
            <p className="mt-0.5">
              Liquid bindings available inside the profile text editors below.
            </p>
          </div>
          <div className="space-y-1 font-mono">
            {PROMPT_VARIABLES.map(([token, description]) => (
              <p key={token}>
                {token} — {description}
              </p>
            ))}
          </div>
        </div>
      </div>

      {isFileBacked ? (
        <div className="grid gap-4 xl:grid-cols-2">
          <MarkdownPromptEditor
            value={soul ?? ''}
            onChange={onSoulChange ?? (() => {})}
            label="SOUL.md"
            placeholder="Describe this profile's stable identity, purpose, boundaries, and collaboration style."
            profile={profileContext}
            helperText={
              <>
                {soulFile && (
                  <span className="font-mono" data-testid="profile-soul-file">
                    {soulFile}
                  </span>
                )}
                {!soulFile && 'Stored in the profile SOUL.md file created by Itervox.'}
              </>
            }
          />
          <MarkdownPromptEditor
            value={instructionsValue}
            onChange={onInstructions}
            label="INSTRUCTIONS.md"
            profile={profileContext}
            placeholder="Write this profile's workflow rules, checklists, and done criteria."
            helperText={
              <>
                {instructionsFile && (
                  <span className="font-mono" data-testid="profile-instructions-file">
                    {instructionsFile}
                  </span>
                )}
                {!instructionsFile &&
                  'Stored in the profile INSTRUCTIONS.md file created by Itervox.'}
              </>
            }
          />
        </div>
      ) : (
        <MarkdownPromptEditor value={prompt} onChange={onPromptChange} profile={profileContext} />
      )}

      <ProfileReferencePicker
        backend={guessProfileBackend(command, backend)}
        onInsert={(reference) => {
          onInstructions(appendReference(instructionsValue, reference));
        }}
      />

      <details
        className="border-theme-line-strong border-t pt-3"
        open={advancedOpen}
        onToggle={(event) => {
          setAdvancedOpen((event.currentTarget as HTMLDetailsElement).open);
        }}
      >
        <summary className="text-theme-text-secondary cursor-pointer text-xs font-semibold">
          Advanced runner command
        </summary>
        <div className="mt-3 space-y-2">
          <input
            value={command}
            onChange={(e) => {
              onCommandChange(e.target.value);
            }}
            placeholder={commandPlaceholder(backend)}
            className={`${selectCls} font-mono text-xs`}
          />
          <p className={helperTextCls}>
            Leave this alone unless you need a wrapper command or a non-standard binary path.
          </p>
          {backend === 'custom' ? (
            <p className={helperTextCls}>
              Custom backend: the command reads the prompt on stdin and prints itervox agent
              protocol events (see the Backends guide).
            </p>
          ) : (
            isCustomCommand && (
              <p className={helperTextCls}>
                Custom command detected — model selection may not apply.
              </p>
            )
          )}
        </div>
      </details>
    </div>
  );
}
