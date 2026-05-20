import { useState } from 'react';
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
import {
  checkboxCls,
  fieldLabelCls,
  fieldSurfaceCls,
  helperTextCls,
  inputCls,
  selectCls,
} from '../formStyles';
import { MarkdownPromptEditor } from './MarkdownPromptEditor';

function ModelInput({
  backend,
  value,
  onChange,
  dynamicModels,
}: {
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
  value,
  onChange,
}: {
  value: SupportedBackend;
  onChange: (value: SupportedBackend) => void;
}) {
  return (
    <select
      value={value}
      onChange={(e) => {
        onChange(normalizeBackend(e.target.value));
      }}
      className={selectCls}
    >
      <option value="claude">Claude</option>
      <option value="codex">Codex</option>
    </select>
  );
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
  allowedActions: AllowedAgentAction[];
  supportedAgentActions?: readonly string[];
  createIssueState: string;
  trackerStates?: readonly string[];
  createIssueStateError?: string;
  onBackendChange: (value: SupportedBackend) => void;
  onModelChange: (value: string) => void;
  onCommandChange: (value: string) => void;
  onPromptChange: (value: string) => void;
  onAllowedActionsChange: (value: AllowedAgentAction[]) => void;
  onCreateIssueStateChange: (value: string) => void;
  dynamicModels?: Record<string, ModelOption[]>;
}

export function ProfileEditorFields({
  backend,
  model,
  command,
  prompt,
  allowedActions,
  supportedAgentActions,
  createIssueState,
  trackerStates,
  createIssueStateError,
  onBackendChange,
  onModelChange,
  onCommandChange,
  onPromptChange,
  onAllowedActionsChange,
  onCreateIssueStateChange,
  dynamicModels,
}: ProfileEditorFieldsProps) {
  const isCustomCommand = !isSimpleBackendCommand(command, backend);
  const canCreateIssue = allowedActions.includes('create_issue');
  const actionOptions = agentActionOptionsFor(supportedAgentActions);
  const [advancedOpen, setAdvancedOpen] = useState(isCustomCommand);
  const [prevIsCustomCommand, setPrevIsCustomCommand] = useState(isCustomCommand);

  if (isCustomCommand !== prevIsCustomCommand) {
    setPrevIsCustomCommand(isCustomCommand);
    if (isCustomCommand) {
      setAdvancedOpen(true);
    }
  }

  return (
    <div className="space-y-4">
      <div className="grid gap-4 md:grid-cols-2">
        <div>
          <label className={fieldLabelCls}>Backend</label>
          <BackendSelect value={backend} onChange={onBackendChange} />
        </div>
        <div>
          <label className={fieldLabelCls}>Model</label>
          <ModelInput
            backend={backend}
            value={model}
            onChange={onModelChange}
            dynamicModels={dynamicModels}
          />
        </div>
      </div>

      <div className="grid gap-4 xl:grid-cols-[minmax(0,1.15fr)_minmax(0,0.85fr)]">
        <div className={fieldSurfaceCls}>
          <div>
            <p className="text-theme-text text-[11px] font-semibold">Daemon Actions</p>
            <p className="text-theme-muted mt-0.5 text-[11px]">
              Daemon-backed actions available to this profile. Invoke them with{' '}
              <span className="font-mono">itervox action ...</span>, not with{' '}
              <span className="font-mono">{'{{ }}'}</span>. Local workers only in v1.
            </p>
          </div>
          <div className="space-y-2">
            {actionOptions.map((option) => {
              const checked = allowedActions.includes(option.id);
              return (
                <label key={option.id} className="flex items-start gap-2 text-[11px]">
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
                    <span className="text-theme-text block font-medium">{option.label}</span>
                    <span className="text-theme-muted block">{option.description}</span>
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
                <p role="alert" className="text-theme-danger text-xs">
                  {createIssueStateError}
                </p>
              )}
            </div>
          )}
        </div>

        <div className={fieldSurfaceCls}>
          <div>
            <p className="text-theme-text text-[11px] font-semibold">Prompt variables</p>
            <p className="text-theme-muted mt-0.5 text-[11px]">
              Liquid bindings available inside the prompt editor below.
            </p>
          </div>
          <div className="space-y-1 font-mono text-[11px]">
            {PROMPT_VARIABLES.map(([token, description]) => (
              <p key={token}>
                {token} — {description}
              </p>
            ))}
          </div>
        </div>
      </div>

      <MarkdownPromptEditor value={prompt} onChange={onPromptChange} />

      <details
        className={fieldSurfaceCls}
        open={advancedOpen}
        onToggle={(event) => {
          setAdvancedOpen((event.currentTarget as HTMLDetailsElement).open);
        }}
      >
        <summary className="text-theme-text cursor-pointer text-[11px] font-semibold">
          Advanced runner command
        </summary>
        <div className="mt-3 space-y-2">
          <input
            value={command}
            onChange={(e) => {
              onCommandChange(e.target.value);
            }}
            placeholder={
              backend === 'codex'
                ? 'codex, /path/to/codex, or a wrapper command'
                : 'claude, /path/to/claude, or a wrapper command'
            }
            className={`${selectCls} font-mono text-xs`}
          />
          <p className={helperTextCls}>
            Leave this alone unless you need a wrapper command or a non-standard binary path.
          </p>
          {isCustomCommand && (
            <p className={helperTextCls}>
              Custom command detected — model selection may not apply.
            </p>
          )}
        </div>
      </details>
    </div>
  );
}
