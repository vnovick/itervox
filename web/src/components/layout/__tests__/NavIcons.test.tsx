import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { AgentsIcon, AutomationsIcon, SettingsIcon } from '../NavIcons';

describe('NavIcons', () => {
  it('renders Lucide sidebar icons for agents, automations, and settings', () => {
    const { container } = render(
      <div>
        <span aria-label="agents">
          <AgentsIcon />
        </span>
        <span aria-label="automations">
          <AutomationsIcon />
        </span>
        <span aria-label="settings">
          <SettingsIcon />
        </span>
      </div>,
    );

    expect(screen.getByLabelText('agents').querySelector('.lucide-bot')).toBeInTheDocument();
    expect(
      screen.getByLabelText('automations').querySelector('.lucide-workflow'),
    ).toBeInTheDocument();
    expect(screen.getByLabelText('settings').querySelector('.lucide-settings')).toBeInTheDocument();
    expect(container.querySelectorAll('svg.lucide')).toHaveLength(3);
  });
});
