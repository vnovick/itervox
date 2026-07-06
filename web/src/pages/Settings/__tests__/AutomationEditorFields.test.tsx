import { render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { AutomationEditorFields } from '../automations/AutomationEditorFields';

describe('AutomationEditorFields', () => {
  it('shows automation-specific variable and filter guidance', () => {
    render(
      <AutomationEditorFields
        values={{
          id: 'input-responder',
          enabled: true,
          profile: 'input-responder',
          instructions: '',
          triggerType: 'input_required',
          triggerState: '',
          cron: '',
          timezone: '',
          matchMode: 'all',
          states: [],
          labelsAny: [],
          identifierRegex: '',
          limit: '',
          inputContextRegex: 'continue|branch',
          maxAgeMinutes: '',
          autoResume: true,
          switchToProfile: '',
          switchToBackend: '',
          cooldownMinutes: '',
          moveToState: '',
        }}
        availableProfiles={['input-responder']}
        availableStates={['Backlog', 'Todo', 'Ready for QA']}
        availableLabels={['triage', 'qa']}
        onEnabledChange={vi.fn()}
        onProfileChange={vi.fn()}
        onInstructionsChange={vi.fn()}
        onTriggerTypeChange={vi.fn()}
        onTriggerStateChange={vi.fn()}
        onCronChange={vi.fn()}
        onTimezoneChange={vi.fn()}
        onMatchModeChange={vi.fn()}
        onStatesChange={vi.fn()}
        onLabelsAnyChange={vi.fn()}
        onIdentifierRegexChange={vi.fn()}
        onLimitChange={vi.fn()}
        onInputContextRegexChange={vi.fn()}
        onMaxAgeMinutesChange={vi.fn()}
        onAutoResumeChange={vi.fn()}
        onSwitchToProfileChange={vi.fn()}
        onSwitchToBackendChange={vi.fn()}
        onCooldownMinutesChange={vi.fn()}
        onMoveToStateChange={vi.fn()}
      />,
    );

    expect(
      screen.getByText(/leave empty to let Itervox use backlog and active states/i),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Automation instructions are rendered with Liquid/i),
    ).toBeInTheDocument();
    expect(screen.getByText(/Instruction templates/i)).toBeInTheDocument();
    expect(screen.getByText(/Match the blocked-agent question text/i)).toBeInTheDocument();
    expect(screen.getByText('{{ trigger.input_context }}')).toBeInTheDocument();
    expect(
      screen.getByText('Match issues that have at least one of these tracker labels.'),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Suggestions come from issues currently visible to Itervox/i),
    ).toBeInTheDocument();
    expect(screen.getByText(/How to combine multiple filters/i)).toBeInTheDocument();
    expect(screen.getByPlaceholderText('+ Add state')).toBeInTheDocument();
    expect(screen.getByPlaceholderText('+ Add label')).toBeInTheDocument();
  });

  it('shows trigger-state selector for issue-entered-state automations', () => {
    render(
      <AutomationEditorFields
        values={{
          id: 'qa-entry',
          enabled: true,
          profile: 'qa',
          instructions: '',
          triggerType: 'issue_entered_state',
          triggerState: 'Ready for QA',
          cron: '',
          timezone: '',
          matchMode: 'all',
          states: [],
          labelsAny: [],
          identifierRegex: '',
          limit: '',
          inputContextRegex: '',
          maxAgeMinutes: '',
          autoResume: false,
          switchToProfile: '',
          switchToBackend: '',
          cooldownMinutes: '',
          moveToState: '',
        }}
        availableProfiles={['qa']}
        availableStates={['Backlog', 'Ready for QA']}
        availableLabels={[]}
        onEnabledChange={vi.fn()}
        onProfileChange={vi.fn()}
        onInstructionsChange={vi.fn()}
        onTriggerTypeChange={vi.fn()}
        onTriggerStateChange={vi.fn()}
        onCronChange={vi.fn()}
        onTimezoneChange={vi.fn()}
        onMatchModeChange={vi.fn()}
        onStatesChange={vi.fn()}
        onLabelsAnyChange={vi.fn()}
        onIdentifierRegexChange={vi.fn()}
        onLimitChange={vi.fn()}
        onInputContextRegexChange={vi.fn()}
        onMaxAgeMinutesChange={vi.fn()}
        onAutoResumeChange={vi.fn()}
        onSwitchToProfileChange={vi.fn()}
        onSwitchToBackendChange={vi.fn()}
        onCooldownMinutesChange={vi.fn()}
        onMoveToStateChange={vi.fn()}
      />,
    );

    expect(screen.getByLabelText('Entered State')).toBeInTheDocument();
    expect(screen.getByDisplayValue('Ready for QA')).toBeInTheDocument();
  });

  it('shows move-to-state policy field for blockers-resolved automations', () => {
    render(
      <AutomationEditorFields
        values={{
          id: 'unblock-backlog-to-todo',
          enabled: true,
          profile: 'pm',
          instructions: '',
          triggerType: 'blockers_resolved',
          triggerState: '',
          cron: '',
          timezone: '',
          matchMode: 'all',
          states: ['Backlog'],
          labelsAny: [],
          identifierRegex: '',
          limit: '',
          inputContextRegex: '',
          maxAgeMinutes: '',
          autoResume: false,
          switchToProfile: '',
          switchToBackend: '',
          cooldownMinutes: '',
          moveToState: 'Todo',
        }}
        availableProfiles={['pm']}
        availableStates={['Backlog', 'Todo', 'Ready for QA']}
        availableLabels={[]}
        onEnabledChange={vi.fn()}
        onProfileChange={vi.fn()}
        onInstructionsChange={vi.fn()}
        onTriggerTypeChange={vi.fn()}
        onTriggerStateChange={vi.fn()}
        onCronChange={vi.fn()}
        onTimezoneChange={vi.fn()}
        onMatchModeChange={vi.fn()}
        onStatesChange={vi.fn()}
        onLabelsAnyChange={vi.fn()}
        onIdentifierRegexChange={vi.fn()}
        onLimitChange={vi.fn()}
        onInputContextRegexChange={vi.fn()}
        onMaxAgeMinutesChange={vi.fn()}
        onAutoResumeChange={vi.fn()}
        onSwitchToProfileChange={vi.fn()}
        onSwitchToBackendChange={vi.fn()}
        onCooldownMinutesChange={vi.fn()}
        onMoveToStateChange={vi.fn()}
      />,
    );

    expect(screen.getByLabelText('Move to state')).toBeInTheDocument();
    expect(screen.getByDisplayValue('Todo')).toBeInTheDocument();
    expect(
      screen.getByText(/requires the selected profile to allow move_state/i),
    ).toBeInTheDocument();
  });
});
