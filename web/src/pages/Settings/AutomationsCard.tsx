import { useMemo, useState } from 'react';
import type { AutomationDef } from '../../types/schemas';
import type { SettingsError } from '../../auth/SettingsError';
import { Card } from '../../components/ui/Card/Card';
import { AutomationFormModal } from './automations/AutomationFormModal';
import { AutomationRow } from './automations/AutomationRow';
import { SuggestedAutomationCard } from './automations/SuggestedAutomationCard';
import {
  SUGGESTED_AUTOMATIONS,
  type SuggestedAutomation,
} from './automations/suggestedAutomations';
import {
  automationDefFromValues,
  automationValuesFromDef,
  automationValuesFromSuggestion,
  emptyAutomationValues,
  type AutomationFormValues,
} from './automations/automationForm';
import {
  MSG_AUTOMATIONS_DUPLICATE_ID,
  MSG_AUTOMATIONS_SAVE_SUCCESS,
  MSG_AUTOMATIONS_TEMPLATE_REQUIRES_PROFILE,
} from './automationMessages';

type AutomationModalState = {
  title: string;
  subtitle?: string;
  submitLabel: string;
  index: number | null;
  initialValues: AutomationFormValues;
};

type AutomationStatus = {
  kind: 'success' | 'error';
  message: string;
};

export function AutomationsCard({
  automations,
  availableProfiles,
  availableStates,
  availableLabels,
  onSave,
  onSaveTyped,
  focusAutomationId,
}: {
  automations: AutomationDef[];
  availableProfiles: string[];
  availableStates: string[];
  availableLabels: string[];
  onSave: (automations: AutomationDef[]) => Promise<boolean>;
  // Optional typed-error save. When provided, AutomationFormModal uses it
  // to surface field-level server validation errors as inline RHF errors
  // instead of toast (T-34). Backwards-compatible: omitting falls back
  // entirely to the boolean onSave path.
  onSaveTyped?: (
    automations: AutomationDef[],
  ) => Promise<{ ok: true } | { ok: false; error: SettingsError | null }>;
  /**
   * T-9: when set, the card scrolls to the matching automation row and
   * opens its edit modal on first render. Used to deep-link from the
   * issue detail slide's "🤖 Triggered by automation X" badge.
   */
  focusAutomationId?: string;
}) {
  const [modalState, setModalState] = useState<AutomationModalState | null>(null);
  const [status, setStatus] = useState<AutomationStatus | null>(null);

  const automationList = useMemo(
    () => [...automations].sort((a, b) => a.id.localeCompare(b.id)),
    [automations],
  );
  const suggestedToShow = useMemo(
    () =>
      SUGGESTED_AUTOMATIONS.filter(
        (suggestion) => !automations.some((item) => item.id === suggestion.id),
      ),
    [automations],
  );

  const openAddModal = () => {
    setModalState({
      title: 'Add Automation',
      subtitle: 'Lightweight cron and event-driven helpers before the full workflow canvas lands.',
      submitLabel: 'Create Automation',
      index: null,
      initialValues: emptyAutomationValues(availableProfiles[0], automations),
    });
  };

  const openEditModal = (index: number, automation: AutomationDef) => {
    setModalState({
      title: `Edit "${automation.id}"`,
      subtitle: 'Update the trigger, selected profile, and automation-specific instructions.',
      submitLabel: 'Save Changes',
      index,
      initialValues: automationValuesFromDef(automation),
    });
  };

  // T-9: open the editor for the focused id once per id change. State is
  // adjusted during render (vs in a useEffect) — React's recommended pattern
  // for "derive state from a prop":
  // https://react.dev/learn/you-might-not-need-an-effect#adjusting-some-state-when-a-prop-changes
  // We only mark the id as handled once findIndex succeeds, so a deep-link
  // that arrives before the automations list loads still opens the modal
  // when the data eventually populates.
  const [lastHandledFocusId, setLastHandledFocusId] = useState<string | undefined>(undefined);
  if (focusAutomationId && focusAutomationId !== lastHandledFocusId) {
    const idx = automationList.findIndex((a) => a.id === focusAutomationId);
    if (idx !== -1) {
      setLastHandledFocusId(focusAutomationId);
      const automation = automationList[idx];
      setModalState({
        title: `Edit "${automation.id}"`,
        subtitle: 'Update the trigger, selected profile, and automation-specific instructions.',
        submitLabel: 'Save Changes',
        index: idx,
        initialValues: automationValuesFromDef(automation),
      });
    }
  }

  const openTemplateModal = (suggestion: SuggestedAutomation) => {
    if (!availableProfiles.includes(suggestion.profile)) {
      setStatus({
        kind: 'error',
        message: MSG_AUTOMATIONS_TEMPLATE_REQUIRES_PROFILE(suggestion.label, suggestion.profile),
      });
      return;
    }
    setModalState({
      title: `Use "${suggestion.label}" Template`,
      subtitle: suggestion.description,
      submitLabel: 'Create Automation',
      index: null,
      initialValues: automationValuesFromSuggestion(suggestion),
    });
  };

  const saveAutomations = async (nextAutomations: AutomationDef[]) => {
    const seen = new Set<string>();
    for (const automation of nextAutomations) {
      const key = automation.id.trim().toLowerCase();
      if (seen.has(key)) {
        setStatus({ kind: 'error', message: MSG_AUTOMATIONS_DUPLICATE_ID });
        return false;
      }
      seen.add(key);
    }
    setStatus(null);
    const ok = await onSave(nextAutomations);
    if (ok) {
      setStatus({ kind: 'success', message: MSG_AUTOMATIONS_SAVE_SUCCESS });
    }
    return ok;
  };

  return (
    <>
      <Card variant="elevated" padding="none" className="overflow-hidden">
        <Card.Header className="bg-theme-panel-strong mb-0 flex items-center justify-between gap-4 px-5 py-4">
          <div>
            <h2 className="text-theme-text text-sm font-semibold">Automations</h2>
            <p className="text-theme-text-secondary mt-0.5 text-xs">
              Cron and event-driven helper rules layered on top of your agent profiles.
            </p>
          </div>
          <button
            onClick={openAddModal}
            disabled={availableProfiles.length === 0}
            className="bg-theme-accent rounded-[var(--radius-sm)] px-4 py-2 text-sm font-medium whitespace-nowrap text-white transition-colors hover:opacity-90 disabled:opacity-50"
          >
            Add Automation
          </button>
        </Card.Header>

        {availableProfiles.length === 0 && (
          <div className="px-5 py-4">
            <p className="text-theme-danger text-xs">
              Create at least one agent profile before adding automations.
            </p>
          </div>
        )}

        {automationList.length === 0 ? (
          <div className="px-5 py-8">
            <div className="border-theme-line bg-theme-bg-soft rounded-[var(--radius-sm)] border border-dashed px-4 py-6 text-center">
              <p className="text-theme-muted text-xs">No automations configured.</p>
            </div>
          </div>
        ) : (
          <div>
            {automationList.map((automation, index) => (
              <AutomationRow
                key={automation.id}
                automation={automation}
                onEdit={() => {
                  openEditModal(index, automation);
                }}
                onDelete={async () => {
                  await saveAutomations(automationList.filter((_, rowIndex) => rowIndex !== index));
                }}
              />
            ))}
          </div>
        )}

        {suggestedToShow.length > 0 && (
          <Card.Footer className="mt-0 px-5 py-4">
            <p className="text-theme-muted mb-3 text-[11px] font-medium tracking-wider uppercase">
              Start From A Template
            </p>
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
              {suggestedToShow.map((suggestion) => (
                <SuggestedAutomationCard
                  key={suggestion.id}
                  suggestion={suggestion}
                  disabled={!availableProfiles.includes(suggestion.profile)}
                  onUse={openTemplateModal}
                />
              ))}
            </div>
          </Card.Footer>
        )}
      </Card>

      {status && (
        <p
          className={
            status.kind === 'error' ? 'text-theme-danger text-sm' : 'text-theme-success text-sm'
          }
        >
          {status.message}
        </p>
      )}

      {modalState && (
        <AutomationFormModal
          key={modalState.initialValues.id}
          isOpen
          title={modalState.title}
          subtitle={modalState.subtitle}
          submitLabel={modalState.submitLabel}
          initialValues={modalState.initialValues}
          availableProfiles={availableProfiles}
          availableStates={availableStates}
          availableLabels={availableLabels}
          onClose={() => {
            setModalState(null);
          }}
          onSubmit={async (values) => {
            const nextAutomation = automationDefFromValues(values);
            const nextAutomations =
              modalState.index === null
                ? [...automationList, nextAutomation]
                : automationList.map((automation, index) =>
                    index === modalState.index ? nextAutomation : automation,
                  );
            // Local duplicate-id pre-check still runs against the in-memory
            // list; that always returns false (boolean) since it's a client
            // bug, not a server-typed error.
            const seen = new Set<string>();
            for (const automation of nextAutomations) {
              const key = automation.id.trim().toLowerCase();
              if (seen.has(key)) {
                setStatus({ kind: 'error', message: MSG_AUTOMATIONS_DUPLICATE_ID });
                return { ok: false, fieldErrors: { id: 'Duplicate automation id' } };
              }
              seen.add(key);
            }
            setStatus(null);

            // Prefer the typed save when supplied — lets the form pin
            // server validation errors to specific inputs (T-34).
            if (onSaveTyped) {
              const result = await onSaveTyped(nextAutomations);
              if (result.ok) {
                setStatus({ kind: 'success', message: MSG_AUTOMATIONS_SAVE_SUCCESS });
                return true;
              }
              if (result.error?.field) {
                return {
                  ok: false,
                  fieldErrors: {
                    [result.error.field]: result.error.message,
                  } as Partial<Record<keyof AutomationFormValues, string>>,
                };
              }
              // No field discriminator — caller's settingsFetchTyped fired
              // no toast (it's the typed variant); surface as a form-level
              // status so the user still sees the message.
              if (result.error) {
                setStatus({ kind: 'error', message: result.error.message });
              }
              return false;
            }

            return saveAutomations(nextAutomations);
          }}
        />
      )}
    </>
  );
}
