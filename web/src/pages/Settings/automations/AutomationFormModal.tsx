import { useId, useState } from 'react';
import { useForm, useWatch } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { Modal, ModalFooter } from '../../../components/ui/modal';
import { fieldLabelCls, inputCls } from '../formStyles';
import { Button } from '../../../components/ui/button';
import { AutomationEditorFields } from './AutomationEditorFields';
import { PR_EVENT_FILTER_FIELDS, PR_EVENT_FILTER_TRIGGER } from './prEventFilters';
import { UnknownValueNotice } from '../../../components/itervox/UnknownValueNotice';
import { unknownAutomationItems } from '../../../types/configRoundTrip';
import { automationFormSchema, type AutomationFormValues } from './automationForm';
import { useTestAutomation } from '../../../queries/automations';

// AutomationSubmitResult is the richer return type onSubmit may produce.
// Backwards-compatible: callers that still return a boolean keep working.
// Callers that want field-level UX return:
//   { ok: false, fieldErrors: { id: "duplicate id", cron: "invalid cron" } }
// and this modal pins each entry to the matching form input via RHF setError.
export type AutomationSubmitResult =
  | boolean
  | {
      ok: false;
      fieldErrors?: Partial<Record<keyof AutomationFormValues, string>>;
      // formError is shown above the submit button when no field discriminator
      // is available — falls back to the toast layer otherwise.
      formError?: string;
    };

export function AutomationFormModal({
  isOpen,
  title,
  subtitle,
  submitLabel,
  initialValues,
  availableProfiles,
  availableStates,
  availableLabels,
  onClose,
  onSubmit,
}: {
  isOpen: boolean;
  title: string;
  subtitle?: string;
  submitLabel: string;
  initialValues: AutomationFormValues;
  availableProfiles: string[];
  availableStates: string[];
  availableLabels: string[];
  onClose: () => void;
  onSubmit: (values: AutomationFormValues) => Promise<AutomationSubmitResult>;
}) {
  const headingId = useId();
  const {
    register,
    handleSubmit,
    control,
    setValue,
    setError,
    formState: { errors, isSubmitting },
  } = useForm<AutomationFormValues>({
    resolver: zodResolver(automationFormSchema),
    defaultValues: initialValues,
  });

  // useWatch subscribes without the RHF watch() function wrapper — compatible
  // with React Compiler memoization (vs the react-hooks/incompatible-library
  // lint warning raised by `watch()`).
  //
  // Runtime invariant: `useForm<AutomationFormValues>({ defaultValues:
  // initialValues })` above seeds the form store with a fully-populated
  // AutomationFormValues object, and the parent AutomationsCard passes
  // `key={initialValues.id}` so this modal remounts per-open. Every field is
  // therefore always present at runtime. `useWatch` returns
  // DeepPartial<AutomationFormValues> purely as a type-safety bound (to
  // accommodate the case where `defaultValues` is missing fields), so the
  // cast here is a true statement about this component's runtime behavior.
  const values = useWatch({ control, defaultValue: initialValues }) as AutomationFormValues;

  const submit = handleSubmit(async (nextValues) => {
    const result = await onSubmit(nextValues);
    if (result === true) {
      onClose();
      return;
    }
    if (result === false) return;
    // Typed error result — pin field-level errors to the matching form
    // inputs via RHF setError so the validation UX matches the existing
    // client-side Zod errors. Persistent across re-renders until the user
    // edits the affected field (RHF clears on change automatically).
    const fieldErrors = result.fieldErrors ?? {};
    for (const [field, message] of Object.entries(fieldErrors)) {
      if (!message) continue;
      setError(field as keyof AutomationFormValues, { type: 'server', message });
    }
    // formError-only response (no field discriminator): the parent's toast
    // path already surfaced the message — nothing more to do here.
  });

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      showCloseButton
      className="mx-4 my-8 max-w-[1024px] rounded-[26px]"
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
          <UnknownValueNotice items={unknownAutomationItems(initialValues.unknownFields)} />
          <AutomationEditorFields
            leadingField={
              <div>
                <label htmlFor="automation-id-input" className={fieldLabelCls}>
                  Automation ID
                </label>
                <input
                  id="automation-id-input"
                  {...register('id')}
                  // eslint-disable-next-line jsx-a11y/no-autofocus -- dialog opens on its primary field; useDialogLayer restores focus to the opener on close (M5-close)
                  autoFocus
                  placeholder="automation-name"
                  className={`${inputCls} font-mono`}
                />
                {errors.id && (
                  <p role="alert" className="text-theme-danger-text mt-1 text-xs">
                    {errors.id.message}
                  </p>
                )}
              </div>
            }
            values={values}
            availableProfiles={availableProfiles}
            availableStates={availableStates}
            availableLabels={availableLabels}
            onEnabledChange={(value) => {
              setValue('enabled', value, { shouldValidate: true });
            }}
            onProfileChange={(value) => {
              setValue('profile', value, { shouldValidate: true });
            }}
            onInstructionsChange={(value) => {
              setValue('instructions', value);
            }}
            onTriggerTypeChange={(value) => {
              setValue('triggerType', value, { shouldValidate: true });
              if (value !== 'issue_entered_state') {
                setValue('triggerState', '');
              }
              if (value !== 'input_required' && value !== 'rate_limited') {
                setValue('autoResume', false);
              }
              if (value !== 'input_required') {
                setValue('inputContextRegex', '');
              }
              // Gap E — clear switch_to_* + cooldown when leaving rate_limited
              if (value !== 'rate_limited') {
                setValue('switchToProfile', '');
                setValue('switchToBackend', '');
                setValue('cooldownMinutes', '');
              }
              if (value !== 'blockers_resolved') {
                setValue('moveToState', '');
              }
              // #173 — each PR-event filter belongs to one trigger.
              for (const field of PR_EVENT_FILTER_FIELDS) {
                if (PR_EVENT_FILTER_TRIGGER[field] !== value) setValue(field, []);
              }
            }}
            onTriggerStateChange={(value) => {
              setValue('triggerState', value, { shouldValidate: true });
            }}
            onCronChange={(value) => {
              setValue('cron', value, { shouldValidate: true });
            }}
            onTimezoneChange={(value) => {
              setValue('timezone', value);
            }}
            onMatchModeChange={(value) => {
              setValue('matchMode', value, { shouldValidate: true });
            }}
            onStatesChange={(value) => {
              setValue('states', value, { shouldValidate: true });
            }}
            onLabelsAnyChange={(value) => {
              setValue('labelsAny', value, { shouldValidate: true });
            }}
            onIdentifierRegexChange={(value) => {
              setValue('identifierRegex', value);
            }}
            onLimitChange={(value) => {
              setValue('limit', value, { shouldValidate: true });
            }}
            onInputContextRegexChange={(value) => {
              setValue('inputContextRegex', value);
            }}
            onMaxAgeMinutesChange={(value) => {
              setValue('maxAgeMinutes', value, { shouldValidate: true });
            }}
            onAutoResumeChange={(value) => {
              setValue('autoResume', value, { shouldValidate: true });
            }}
            onSwitchToProfileChange={(value) => {
              setValue('switchToProfile', value, { shouldValidate: true });
            }}
            onSwitchToBackendChange={(value) => {
              setValue('switchToBackend', value, { shouldValidate: true });
            }}
            onCooldownMinutesChange={(value) => {
              setValue('cooldownMinutes', value, { shouldValidate: true });
            }}
            onMoveToStateChange={(value) => {
              setValue('moveToState', value, { shouldValidate: true });
            }}
            onPREventFilterChange={(field, value) => {
              setValue(field, value, { shouldValidate: true });
            }}
          />

          {errors.profile && (
            <p role="alert" className="text-theme-danger-text text-xs">
              {errors.profile.message}
            </p>
          )}
          {errors.cron && (
            <p role="alert" className="text-theme-danger-text text-xs">
              {errors.cron.message}
            </p>
          )}
          {errors.triggerState && (
            <p role="alert" className="text-theme-danger-text text-xs">
              {errors.triggerState.message}
            </p>
          )}
          {errors.limit && (
            <p role="alert" className="text-theme-danger-text text-xs">
              {errors.limit.message}
            </p>
          )}

          <ModalFooter divider>
            <TestFireControl
              automationId={values.id}
              disabled={isSubmitting || !values.id.trim()}
            />
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

/**
 * Inline mini-form embedded in the AutomationFormModal footer (T-10). Lets the
 * operator pick a target issue identifier and POST to
 * /api/v1/automations/{id}/test without leaving the editor. Disabled until the
 * automation has a non-empty id (the rule must exist server-side; saving the
 * editor is what materialises a freshly-created rule).
 */
function TestFireControl({ automationId, disabled }: { automationId: string; disabled: boolean }) {
  const [identifier, setIdentifier] = useState('');
  const mutation = useTestAutomation();
  const onClick = () => {
    const trimmed = identifier.trim();
    if (!trimmed) return;
    mutation.mutate({ automationId, identifier: trimmed });
  };
  return (
    <div data-testid="automation-test-fire" className="mr-auto flex items-center gap-2">
      <input
        type="text"
        placeholder="ENG-1"
        value={identifier}
        onChange={(e) => {
          setIdentifier(e.target.value);
        }}
        className={`${inputCls} w-40 font-mono text-[13px]`}
        aria-label="Issue identifier to test-fire against"
      />
      <Button
        variant="glass"
        size="lg"
        onClick={onClick}
        disabled={disabled || mutation.isPending || !identifier.trim()}
        title={
          disabled
            ? 'Save the rule first to enable test fire'
            : 'Dispatch a one-off run of this automation'
        }
      >
        {mutation.isPending ? 'Firing…' : 'Test fire'}
      </Button>
    </div>
  );
}
