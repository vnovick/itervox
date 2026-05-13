import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { RateLimitedFieldsBlock } from '../RateLimitedFieldsBlock';
import type { AutomationFormValues } from '../automationForm';

const baseValues: AutomationFormValues = {
  id: 'rate-limit-fallback',
  enabled: true,
  profile: 'fallback-codex',
  instructions: '',
  triggerType: 'rate_limited',
  triggerState: '',
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
};

describe('RateLimitedFieldsBlock', () => {
  it('renders rate-limit policy controls and forwards edits', () => {
    const onSwitchToProfileChange = vi.fn();
    const onSwitchToBackendChange = vi.fn();
    const onCooldownMinutesChange = vi.fn();
    const onAutoResumeChange = vi.fn();

    render(
      <RateLimitedFieldsBlock
        values={baseValues}
        availableProfiles={['fallback-codex', 'claude-coder']}
        onSwitchToProfileChange={onSwitchToProfileChange}
        onSwitchToBackendChange={onSwitchToBackendChange}
        onCooldownMinutesChange={onCooldownMinutesChange}
        onAutoResumeChange={onAutoResumeChange}
      />,
    );

    fireEvent.change(screen.getByLabelText('Switch to profile'), {
      target: { value: 'claude-coder' },
    });
    fireEvent.change(screen.getByLabelText('Override backend (optional)'), {
      target: { value: 'codex' },
    });
    fireEvent.change(screen.getByLabelText('Cooldown (minutes)'), {
      target: { value: '45' },
    });
    fireEvent.click(screen.getByLabelText(/auto-switch/i));

    expect(onSwitchToProfileChange).toHaveBeenCalledWith('claude-coder');
    expect(onSwitchToBackendChange).toHaveBeenCalledWith('codex');
    expect(onCooldownMinutesChange).toHaveBeenCalledWith('45');
    expect(onAutoResumeChange).toHaveBeenCalledWith(true);
  });

  it('warns when the saved switch profile no longer exists', () => {
    render(
      <RateLimitedFieldsBlock
        values={{ ...baseValues, switchToProfile: 'deleted-profile' }}
        availableProfiles={['fallback-codex']}
        onSwitchToProfileChange={vi.fn()}
        onSwitchToBackendChange={vi.fn()}
        onCooldownMinutesChange={vi.fn()}
        onAutoResumeChange={vi.fn()}
      />,
    );

    expect(screen.getByTestId('rate-limited-missing-profile-warning')).toHaveTextContent(
      'deleted-profile',
    );
  });
});
