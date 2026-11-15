import { useId } from 'react';
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
import { checkboxCls, fieldLabelCls, helperTextCls, inputCls } from '../formStyles';
import { Button } from '../../../components/ui/button';
import { ProfileEditorFields } from './ProfileEditorFields';
import { UnknownValueNotice } from '../../../components/itervox/UnknownValueNotice';
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
      soul?: string;
      instructions?: string;
      soulFile?: string;
      instructionsFile?: string;
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
  const headingId = useId();
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
  const [
    name,
    enabled,
    backend,
    model,
    command,
    prompt,
    soul,
    instructions,
    soulFile,
    instructionsFile,
    allowedActions,
    createIssueState,
  ] = useWatch({
    control,
    name: [
      'name',
      'enabled',
      'backend',
      'model',
      'command',
      'prompt',
      'soul',
      'instructions',
      'soulFile',
      'instructionsFile',
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
        prompt: values.instructions.trim() || values.prompt.trim() || undefined,
        soul: values.soul,
        instructions: values.instructions,
        soulFile: values.soulFile || undefined,
        instructionsFile: values.instructionsFile || undefined,
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
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      showCloseButton
      className="mx-4 my-6 max-w-[1024px] rounded-[26px]"
      ariaLabelledBy={headingId}
    >
      <div className="space-y-4 px-6 py-[22px]">
        <div className="pr-12">
          <h2 id={headingId} className="text-theme-text text-xl font-bold">
            {title}
          </h2>
          {subtitle && <p className="field-help mt-1">{subtitle}</p>}
        </div>

        <form
          onSubmit={(event) => {
            void submit(event);
          }}
          className="space-y-4"
        >
          <UnknownValueNotice
            items={(initialValues.unknownAllowedActions ?? []).map((raw) => ({
              what: 'action',
              raw,
            }))}
          />
          <ProfileEditorFields
            backend={backend}
            model={model}
            command={command}
            prompt={prompt}
            soul={soul}
            instructions={instructions}
            soulFile={soulFile}
            instructionsFile={instructionsFile}
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
              setValue('instructions', value);
            }}
            onSoulChange={(value) => {
              setValue('soul', value);
            }}
            onInstructionsChange={(value) => {
              setValue('instructions', value);
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
            afterFirstRow={
              <label className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
                <input
                  type="checkbox"
                  checked={enabled}
                  onChange={(event) => {
                    setValue('enabled', event.target.checked, { shouldValidate: true });
                  }}
                  className={checkboxCls}
                />
                <span className="text-theme-text">Active profile</span>
                <span className="field-help">
                  Inactive profiles stay in settings but are hidden from dispatch selectors and
                  automations.
                </span>
              </label>
            }
            leadingField={
              <div>
                <label htmlFor="profile-name-input" className={fieldLabelCls}>
                  Profile Name
                </label>
                <input
                  id="profile-name-input"
                  {...register('name')}
                  // eslint-disable-next-line jsx-a11y/no-autofocus -- dialog opens on its primary field; useDialogLayer restores focus to the opener on close (M5-close)
                  autoFocus
                  placeholder="profile-name"
                  className={inputCls}
                />
                {errors.name && (
                  <p role="alert" className="text-theme-danger-text mt-1 text-xs">
                    {errors.name.message}
                  </p>
                )}
                {mode === 'edit' && (
                  <p className={helperTextCls}>Renaming updates the stored profile key.</p>
                )}
              </div>
            }
            dynamicModels={availableModels}
            profileName={name}
          />

          {errors.command && (
            <p role="alert" className="text-theme-danger-text text-xs">
              {errors.command.message}
            </p>
          )}
          {errors.root && (
            <p role="alert" className="text-theme-danger-text text-xs">
              {errors.root.message}
            </p>
          )}

          <ModalFooter divider>
            <Button variant="glass" size="lg" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" size="lg" disabled={isSubmitting}>
              {isSubmitting ? 'Saving…' : submitLabel}
            </Button>
          </ModalFooter>
        </form>
      </div>
    </Modal>
  );
}
