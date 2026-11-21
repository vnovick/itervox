import { act, renderHook } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { useUIStore } from '../../../../store/uiStore';
import { useLanesTabRequest } from '../useLanesTabRequest';

describe('useLanesTabRequest (guardrails.lanes)', () => {
  beforeEach(() => {
    useUIStore.setState({ issueLanesFor: null });
  });

  it('switches the shown issue to its Changes tab when its Lanes view is asked for', () => {
    const setTab = vi.fn();
    renderHook(() => {
      useLanesTabRequest('ENG-1', setTab);
    });
    expect(setTab).not.toHaveBeenCalled();
    act(() => {
      useUIStore.getState().setIssueLanesFor('ENG-2');
    });
    expect(setTab).not.toHaveBeenCalled();
    act(() => {
      useUIStore.getState().setIssueLanesFor('ENG-1');
    });
    expect(setTab).toHaveBeenCalledWith('changes');
  });
});
