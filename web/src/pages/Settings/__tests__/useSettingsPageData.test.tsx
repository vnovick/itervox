import { renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useItervoxStore } from '../../../store/itervoxStore';
import { makeAutomation, makeProfileDef, makeSnapshot } from '../../../test/fixtures/snapshots';
import { resetAllStores } from '../../../test/resetStores';
import { useSettingsPageData } from '../useSettingsPageData';

const queryMocks = vi.hoisted(() => ({
  useIssues: vi.fn(),
}));

const actionMocks = vi.hoisted(() => ({
  setInlineInput: vi.fn(),
}));

vi.mock('../../../queries/issues', () => queryMocks);
vi.mock('../../../hooks/useSettingsActions', () => ({
  useSettingsActions: () => actionMocks,
}));

beforeEach(() => {
  queryMocks.useIssues.mockReturnValue({ data: [] });
  resetAllStores();
});

afterEach(() => {
  resetAllStores();
});

describe('useSettingsPageData', () => {
  it('derives sorted state, label, automation profile, and reviewer options', () => {
    queryMocks.useIssues.mockReturnValue({
      data: [
        { state: 'Needs Review', labels: ['zeta', 'triage'] },
        { state: 'Backlog', labels: ['triage', 'alpha'] },
      ],
    });
    useItervoxStore.setState({
      snapshot: makeSnapshot({
        backlogStates: ['Backlog'],
        activeStates: ['Todo'],
        terminalStates: ['Done'],
        completionState: 'Done',
        availableProfiles: ['default'],
        profileDefs: {
          default: makeProfileDef(),
          reviewer: makeProfileDef(),
          automation: makeProfileDef(),
        },
        reviewerProfile: 'reviewer',
        automations: [makeAutomation({ id: 'auto', profile: 'automation' })],
      }),
    });

    const { result } = renderHook(() => useSettingsPageData());

    expect(result.current.trackerStateOptions).toEqual(['Backlog', 'Done', 'Needs Review', 'Todo']);
    expect(result.current.automationLabelOptions).toEqual(['alpha', 'triage', 'zeta']);
    expect(result.current.automationProfileOptions).toEqual(['automation', 'default']);
    expect(result.current.reviewerProfileOptions).toEqual(['default', 'reviewer']);
    expect(result.current.setInlineInput).toBe(actionMocks.setInlineInput);
  });

  it('falls back to stable defaults when the snapshot is empty', () => {
    useItervoxStore.setState({ snapshot: null });

    const { result } = renderHook(() => useSettingsPageData());

    expect(result.current.activeStates).toEqual([]);
    expect(result.current.maxRetries).toBe(5);
    expect(result.current.maxSwitchesPerIssuePerWindow).toBe(2);
    expect(result.current.switchWindowHours).toBe(6);
    expect(result.current.automationProfileOptions).toEqual([]);
  });
});
