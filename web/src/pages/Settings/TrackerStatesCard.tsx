import { useEffect, useRef, useState } from 'react';
import { useForm, useWatch } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { SAVE_OK_BANNER_MS } from '../../utils/timings';
import { TagInput } from '../../components/itervox/TagInput';

// ─── Zod schema ──────────────────────────────────────────────────────────────

export const trackerStatesSchema = z
  .object({
    activeStates: z.array(z.string().min(1)).min(1, 'At least one active state is required.'),
    terminalStates: z.array(z.string().min(1)),
    completionState: z.string(),
  })
  .refine((d) => !d.activeStates.some((s) => d.terminalStates.includes(s)), {
    message: 'Active and terminal states must not overlap.',
    path: ['terminalStates'],
  });

export type TrackerStatesFormValues = z.infer<typeof trackerStatesSchema>;

// ─── Component ───────────────────────────────────────────────────────────────

interface TrackerStatesCardProps {
  initialActiveStates: string[];
  initialTerminalStates: string[];
  initialCompletionState: string;
  onSave: (
    activeStates: string[],
    terminalStates: string[],
    completionState: string,
  ) => Promise<boolean>;
}

export function TrackerStatesCard({
  initialActiveStates,
  initialTerminalStates,
  initialCompletionState,
  onSave,
}: TrackerStatesCardProps) {
  const {
    register,
    handleSubmit,
    control,
    setValue,
    reset,
    formState: { errors, isSubmitting },
  } = useForm<TrackerStatesFormValues>({
    resolver: zodResolver(trackerStatesSchema),
    defaultValues: {
      activeStates: initialActiveStates,
      terminalStates: initialTerminalStates,
      completionState: initialCompletionState,
    },
  });

  // Sync form when server state changes (e.g. after another client saves)
  useEffect(() => {
    reset({
      activeStates: initialActiveStates,
      terminalStates: initialTerminalStates,
      completionState: initialCompletionState,
    });
  }, [initialActiveStates, initialTerminalStates, initialCompletionState, reset]);

  // useWatch subscribes without the `watch()` function wrapper so the
  // component is compatible with React Compiler memoization.
  const activeStates = useWatch({ control, name: 'activeStates' });
  const terminalStates = useWatch({ control, name: 'terminalStates' });

  const [saveOk, setSaveOk] = useState(false);
  const [saveError, setSaveError] = useState('');
  const saveOkTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    return () => {
      if (saveOkTimerRef.current !== null) clearTimeout(saveOkTimerRef.current);
    };
  }, []);

  const doSubmit = async (values: TrackerStatesFormValues) => {
    setSaveError('');
    setSaveOk(false);
    const ok = await onSave(values.activeStates, values.terminalStates, values.completionState);
    if (ok) {
      setSaveOk(true);
      saveOkTimerRef.current = setTimeout(() => {
        setSaveOk(false);
        saveOkTimerRef.current = null;
      }, SAVE_OK_BANNER_MS);
    } else {
      setSaveError('Failed to save. Check the server logs.');
    }
  };

  return (
    <div className="border-theme-line bg-theme-bg-elevated overflow-hidden rounded-[var(--radius-md)] border">
      <div className="border-theme-line bg-theme-panel-strong border-b px-5 py-4">
        <h2 className="text-theme-text text-sm font-semibold">Tracker States</h2>
        <p className="text-theme-text-secondary mt-0.5 text-xs">
          Configure which states the orchestrator picks up (Active), marks as done (Terminal), and
          transitions to on completion. Changes are written back to WORKFLOW.md.
        </p>
      </div>

      <form
        onSubmit={(e) => {
          void handleSubmit(doSubmit)(e);
        }}
        className="space-y-5 px-5 py-5"
      >
        <div>
          <label className="mb-2 block text-xs font-medium tracking-wider uppercase">
            Active States
          </label>
          <TagInput
            chips={activeStates}
            onChange={(chips) => {
              setValue('activeStates', chips, { shouldValidate: true });
            }}
            chipClassName="bg-[var(--accent-soft)] text-[var(--accent-strong)]"
            addButtonClassName="bg-[var(--accent-soft)] text-[var(--accent-strong)] hover:opacity-80"
          />
          {errors.activeStates && (
            <p role="alert" className="text-theme-danger mt-1 text-xs">
              {errors.activeStates.message}
            </p>
          )}
        </div>

        <div>
          <label className="mb-2 block text-xs font-medium tracking-wider uppercase">
            Terminal States
          </label>
          <TagInput
            chips={terminalStates}
            onChange={(chips) => {
              setValue('terminalStates', chips, { shouldValidate: true });
            }}
            chipClassName="bg-[var(--bg-soft)] text-[var(--text-secondary)]"
            addButtonClassName="bg-[var(--bg-soft)] text-[var(--text-secondary)] hover:opacity-80"
          />
          {errors.terminalStates && (
            <p role="alert" className="text-theme-danger mt-1 text-xs">
              {errors.terminalStates.message}
            </p>
          )}
        </div>

        <div>
          <label className="mb-2 block text-xs font-medium tracking-wider uppercase">
            Completion State
          </label>
          <input
            type="text"
            placeholder="e.g. In Review (leave empty to skip)"
            className="border-theme-line bg-theme-panel-strong text-theme-text w-64 rounded-[var(--radius-sm)] border px-3 py-2 text-[13px] focus:outline-none"
            {...register('completionState')}
          />
          <p className="text-theme-muted mt-1 text-xs">
            The state the agent moves an issue to when it finishes successfully. Has to be 1:1 with
            a tracker state.
          </p>
        </div>

        <div className="flex items-center gap-3 pt-1">
          <button
            type="submit"
            disabled={isSubmitting}
            className="bg-theme-accent rounded-[var(--radius-sm)] px-4 py-2 text-sm font-medium text-white transition-colors disabled:opacity-50"
          >
            {isSubmitting ? 'Saving…' : 'Save Changes'}
          </button>
          {saveOk && <span className="text-theme-success text-sm">Saved successfully.</span>}
          {saveError && <span className="text-theme-danger text-sm">{saveError}</span>}
        </div>
      </form>
    </div>
  );
}
