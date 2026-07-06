import { fieldSurfaceCls } from '../formStyles';
import {
  INSTRUCTION_TEMPLATES,
  VARIABLE_GROUPS,
  type AutomationTriggerType,
} from './automationEditorConstants';

// AutomationInstructionsPanel renders the side-by-side "Instruction templates"
// and "Prompt variables" cards that sit above the Markdown editor in the
// automation editor. Extracted from AutomationEditorFields to keep that file
// under the size-budget cap (T-57). Pure presentational; the parent owns the
// instructions form value and forwards onInstructionsChange.
export function AutomationInstructionsPanel({
  triggerType,
  onInstructionsChange,
}: {
  triggerType: AutomationTriggerType;
  onInstructionsChange: (value: string) => void;
}) {
  const visibleTemplates = INSTRUCTION_TEMPLATES.filter(
    (template) => !template.triggerTypes || template.triggerTypes.includes(triggerType),
  );

  return (
    <div className="grid gap-4 xl:grid-cols-[minmax(0,1.05fr)_minmax(0,0.95fr)]">
      <div className={fieldSurfaceCls}>
        <div>
          <p className="text-theme-text text-[11px] font-semibold">Instruction templates</p>
          <p className="text-theme-muted mt-0.5 text-[11px]">
            Start from a reusable instruction block, then tailor it to the selected profile and
            trigger.
          </p>
        </div>
        <div className="grid gap-2 sm:grid-cols-2">
          {visibleTemplates.map((template) => (
            <button
              key={template.id}
              type="button"
              onClick={() => {
                onInstructionsChange(template.instruction);
              }}
              className="border-theme-line bg-theme-panel hover:bg-theme-bg-soft rounded-[var(--radius-sm)] border p-3 text-left transition-colors"
            >
              <p className="text-theme-text text-xs font-medium">{template.label}</p>
              <p className="text-theme-muted mt-1 text-[11px] leading-relaxed">
                {template.description}
              </p>
            </button>
          ))}
        </div>
      </div>

      <div className={fieldSurfaceCls}>
        <div>
          <p className="text-theme-text text-[11px] font-semibold">Prompt variables</p>
          <p className="text-theme-muted mt-0.5 text-[11px]">
            Liquid bindings available to this automation. Trigger variables depend on the selected
            trigger type.
          </p>
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          {VARIABLE_GROUPS.map((group) => (
            <div key={group.title} className="space-y-1">
              <p className="text-theme-text text-[11px] font-medium">{group.title}</p>
              <div className="space-y-1 font-mono text-[11px]">
                {group.values.map((value) => (
                  <p key={value}>{value}</p>
                ))}
              </div>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
