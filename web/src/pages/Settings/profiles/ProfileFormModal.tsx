import { useForm, useWatch } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { Modal, ModalFooter } from '../../../components/ui/modal';
import {
  applyBackendSelection,
  applyModelSelection,
  commandToModel,
  inferBackendFromCommand,
  normalizeAllowedActions,
  normalizeCommandForSave,
} from '../profileCommands';
import { checkboxCls, helperTextCls, inputCls } from '../formStyles';
import { ProfileEditorFields } from './ProfileEditorFields';
import { type ProfileFormValues, profileFormSchema } from './profileForm';

interface ProfileFormModalProps {
  isOpen: boolean;
  mode: 'add' | 'edit';
  title: string;
  subtitle?: string;
  initialValues: ProfileFormValues;
  submitLabel: string;
  availableModels?: Record<string, { id: string; label: string }[]>;
  supportedAgentActions?: readonly string[];
  trackerStates?: readonly string[];
  onClose: () => void;
  onSubmit: (
    name: string,
    def: {
      command: string;
      backend?: string;
      prompt?: string;
      enabled: boolean;
      allowedActions?: string[];
      createIssueState?: string;
    },
    originalName?: string,
  ) => Promise<boolean>;
}

export function ProfileFormModal({
  isOpen,
  mode,
  title,
  subtitle,
  initialValues,
  submitLabel,
  availableModels,
  supportedAgentActions,
  trackerStates,
  onClose,
  onSubmit,
}: ProfileFormModalProps) {
  const {
    register,
    handleSubmit,
    control,
    setValue,
    formState: { errors, isSubmitting },
  } = useForm<ProfileFormValues>({
    resolver: zodResolver(profileFormSchema),
    defaultValues: initialValues,
  });

  // useWatch subscribes without the `watch()` function wrapper so the
  // component is compatible with React Compiler memoization. The array-name
  // overload returns each field in the order listed in `name`, typed per the
  // shape of ProfileFormValues (non-partial, because useForm.defaultValues
  // above is fully populated).
  const [enabled, backend, model, command, prompt, allowedActions, createIssueState] = useWatch({
    control,
    name: [
      'enabled',
      'backend',
      'model',
      'command',
      'prompt',
      'allowedActions',
      'createIssueState',
    ],
  });

  const submit = handleSubmit(async (values) => {
    const ok = await onSubmit(
      values.name.trim(),
      {
        command: normalizeCommandForSave(values.command, values.backend),
        backend: values.backend,
        prompt: values.prompt.trim() || undefined,
        enabled: values.enabled,
        allowedActions: normalizeAllowedActions(values.allowedActions, supportedAgentActions),
        createIssueState: values.createIssueState.trim() || undefined,
      },
      mode === 'edit' ? initialValues.name : undefined,
    );
    if (ok) {
      onClose();
    }
  });

  return (
    <Modal isOpen={isOpen} onClose={onClose} showCloseButton padded className="mx-4 my-6 max-w-5xl">
      <div className="space-y-5">
        <div>
          <h2 className="text-theme-text text-lg font-semibold">{title}</h2>
          {subtitle && <p className="text-theme-text-secondary mt-1 text-sm">{subtitle}</p>}
        </div>

        <form
          onSubmit={(event) => {
            void submit(event);
          }}
          className="space-y-4"
        >
          <div>
            <label
              htmlFor="profile-name-input"
              className="text-theme-text-secondary mb-2 block text-xs font-medium tracking-wider uppercase"
            >
              Profile Name
            </label>
            <input
              id="profile-name-input"
              {...register('name')}
              autoFocus
              placeholder="profile-name"
              className={`${inputCls} font-mono text-sm`}
            />
            {errors.name && (
              <p role="alert" className="text-theme-danger mt-1 text-xs">
                {errors.name.message}
              </p>
            )}
            {mode === 'edit' && (
              <p className={helperTextCls}>Renaming updates the stored profile key.</p>
            )}
          </div>

          <label className="border-theme-line bg-theme-bg-soft flex items-start gap-3 rounded-[var(--radius-sm)] border px-3 py-3">
            <input
              type="checkbox"
              checked={enabled}
              onChange={(event) => {
                setValue('enabled', event.target.checked, { shouldValidate: true });
              }}
              className={checkboxCls}
            />
            <span className="min-w-0">
              <span className="text-theme-text block text-sm font-medium">Active profile</span>
              <span className="text-theme-text-secondary block text-xs">
                Inactive profiles stay in settings but are hidden from dispatch selectors and
                automations.
              </span>
            </span>
          </label>

          <ProfileEditorFields
            backend={backend}
            model={model}
            command={command}
            prompt={prompt}
            allowedActions={allowedActions}
            supportedAgentActions={supportedAgentActions}
            createIssueState={createIssueState}
            createIssueStateError={errors.createIssueState?.message}
            trackerStates={trackerStates}
            onBackendChange={(value) => {
              const next = applyBackendSelection(command, backend, value);
              setValue('backend', value, { shouldValidate: true });
              setValue('model', next.model);
              setValue('command', next.command, { shouldValidate: true });
            }}
            onModelChange={(value) => {
              setValue('model', value);
              setValue('command', applyModelSelection(command, backend, value), {
                shouldValidate: true,
              });
            }}
            onCommandChange={(value) => {
              setValue('command', value, { shouldValidate: true });
              setValue('model', commandToModel(value));
              const inferred = inferBackendFromCommand(value);
              if (inferred) setValue('backend', inferred);
            }}
            onPromptChange={(value) => {
              setValue('prompt', value);
            }}
            onAllowedActionsChange={(value) => {
              setValue('allowedActions', value, { shouldValidate: true });
              if (!value.includes('create_issue')) {
                setValue('createIssueState', '', { shouldValidate: true });
              }
            }}
            onCreateIssueStateChange={(value) => {
              setValue('createIssueState', value, { shouldValidate: true });
            }}
            dynamicModels={availableModels}
          />

          {errors.command && (
            <p role="alert" className="text-theme-danger text-xs">
              {errors.command.message}
            </p>
          )}
          {errors.root && (
            <p role="alert" className="text-theme-danger text-xs">
              {errors.root.message}
            </p>
          )}

          <ModalFooter>
            <button
              type="button"
              onClick={onClose}
              className="border-theme-line text-theme-text-secondary rounded-[var(--radius-sm)] border px-4 py-2 text-sm transition-colors hover:opacity-80"
            >
              Cancel
            </button>
            <button
              type="submit"
              disabled={isSubmitting}
              className="bg-theme-accent rounded-[var(--radius-sm)] px-4 py-2 text-sm font-medium text-white transition-colors disabled:opacity-50"
            >
              {isSubmitting ? 'Saving…' : submitLabel}
            </button>
          </ModalFooter>
        </form>
      </div>
    </Modal>
  );
}
