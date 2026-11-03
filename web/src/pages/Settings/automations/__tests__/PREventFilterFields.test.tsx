import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { PREventFilterFields } from '../PREventFilterFields';
import {
  emptyAutomationValues,
  automationDefFromValues,
  automationValuesFromDef,
} from '../automationForm';
import type { AutomationDef } from '../../../../types/schemas';

describe('PREventFilterFields (#173)', () => {
  it('shows only the selected trigger’s filters and forwards edits', () => {
    const onChange = vi.fn();
    const values = {
      ...emptyAutomationValues('fixer', []),
      triggerType: 'pr_check_completed' as const,
      checkNames: ['CI'],
    };
    render(<PREventFilterFields values={values} onChange={onChange} />);

    expect(screen.getByLabelText('Check names')).toBeInTheDocument();
    expect(screen.getByLabelText('Conclusions')).toBeInTheDocument();
    expect(screen.queryByLabelText('Review authors')).not.toBeInTheDocument();
    expect(screen.getByText('CI')).toBeInTheDocument();

    const input = screen.getByLabelText('Conclusions');
    fireEvent.change(input, { target: { value: 'failure' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(onChange).toHaveBeenCalledWith('conclusions', ['failure']);
  });

  it('shows the review filters for pr_review_submitted', () => {
    const values = {
      ...emptyAutomationValues('fixer', []),
      triggerType: 'pr_review_submitted' as const,
    };
    render(<PREventFilterFields values={values} onChange={vi.fn()} />);
    expect(screen.getByLabelText('Review authors')).toBeInTheDocument();
    expect(screen.getByLabelText('Review states')).toBeInTheDocument();
    expect(screen.queryByLabelText('Check names')).not.toBeInTheDocument();
  });
});

describe('PR-event filter round trip (#173)', () => {
  it('keeps a PR-event rule’s own filters and drops the others', () => {
    const def: AutomationDef = {
      id: 'greptile-fix',
      enabled: true,
      profile: 'fixer',
      trigger: { type: 'pr_review_submitted' },
      filter: { reviewAuthors: ['greptile-apps[bot]'], reviewStates: ['commented'] },
    };
    const values = automationValuesFromDef(def);
    expect(values.reviewAuthors).toEqual(['greptile-apps[bot]']);
    expect(values.checkNames).toEqual([]);
    expect(automationDefFromValues(values).filter).toEqual({
      reviewAuthors: ['greptile-apps[bot]'],
      reviewStates: ['commented'],
    });

    const switched = automationDefFromValues({ ...values, triggerType: 'pr_check_completed' });
    expect(switched.filter).toBeUndefined();
  });
});
