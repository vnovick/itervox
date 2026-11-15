// Visual refresh — the profile and cron editors take layout slots so the
// dialogs can put the name / id beside backend / trigger and the timezone
// beside the cron expression.
import { describe, expect, it, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { ProfileEditorFields } from '../profiles/ProfileEditorFields';
import { CronPicker } from '../automations/CronPicker';

vi.mock('../../../queries/skills', () => ({ useSkillsInventory: () => ({ data: undefined }) }));

const noop = () => {};

describe('ProfileEditorFields slots', () => {
  it('renders the leading field in a three-column first row, then afterFirstRow', () => {
    const { container } = render(
      <ProfileEditorFields
        backend="claude"
        model=""
        command="claude"
        prompt=""
        allowedActions={[]}
        createIssueState=""
        onBackendChange={noop}
        onModelChange={noop}
        onCommandChange={noop}
        onPromptChange={noop}
        onAllowedActionsChange={noop}
        onCreateIssueStateChange={noop}
        leadingField={<div data-testid="lead">name</div>}
        afterFirstRow={<div data-testid="after">active</div>}
      />,
    );
    const row = screen.getByTestId('lead').parentElement as HTMLElement;
    expect(row).toHaveClass('md:grid-cols-3');
    expect(within(row).getByLabelText('Backend')).toBeInTheDocument();
    expect(row.nextElementSibling).toBe(screen.getByTestId('after'));
    expect(container.querySelector('fieldset > legend')).toHaveTextContent('Daemon Actions');
  });

  it('keeps two columns without a leading field', () => {
    render(
      <ProfileEditorFields
        backend="claude"
        model=""
        command="claude"
        prompt=""
        allowedActions={[]}
        createIssueState=""
        onBackendChange={noop}
        onModelChange={noop}
        onCommandChange={noop}
        onPromptChange={noop}
        onAllowedActionsChange={noop}
        onCreateIssueStateChange={noop}
      />,
    );
    const backend = screen.getByLabelText('Backend');
    expect(backend.closest('.grid')).toHaveClass('md:grid-cols-2');
  });
});

describe('CronPicker slots', () => {
  it('renders the title inside the group and the aside beside the expression', () => {
    render(
      <CronPicker
        value="0 9 * * 1-5"
        onChange={noop}
        labelledBy="cron-title"
        title={<p id="cron-title">Cron</p>}
        aside={<div data-testid="tz">tz</div>}
      />,
    );
    const group = screen.getByRole('group', { name: 'Cron' });
    expect(group).toHaveClass('field-group');
    const raw = screen.getByTestId('cron-raw-input');
    expect(raw.closest('.grid')).toContainElement(screen.getByTestId('tz'));
  });
});
