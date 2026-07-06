import { describe, expect, it } from 'vitest';
import {
  inputRequiredFingerprintValue,
  inputRequiredRowState,
  PENDING_RESUME_CONTEXT_PREFIX,
} from '../inputRequired';

describe('inputRequiredRowState', () => {
  it('uses explicit pending resume state when present', () => {
    expect(
      inputRequiredRowState({
        state: 'pending_input_resume',
        context: 'anything',
      }),
    ).toBe('pending_input_resume');
  });

  it('uses explicit input required state when present', () => {
    expect(
      inputRequiredRowState({
        state: 'input_required',
        context: `${PENDING_RESUME_CONTEXT_PREFIX}\n\nOriginal request:\nNeed approval`,
      }),
    ).toBe('input_required');
  });

  it('falls back to legacy pending resume context when state is missing', () => {
    expect(
      inputRequiredRowState({
        state: undefined as never,
        context: `${PENDING_RESUME_CONTEXT_PREFIX}\n\nOriginal request:\nNeed approval`,
      }),
    ).toBe('pending_input_resume');
  });
});

describe('inputRequiredFingerprintValue', () => {
  it('changes when the row state changes for the same identifier', () => {
    const waiting = inputRequiredFingerprintValue({
      identifier: 'ENG-1',
      state: 'input_required',
      context: 'Need approval',
    });
    const pending = inputRequiredFingerprintValue({
      identifier: 'ENG-1',
      state: 'pending_input_resume',
      context: `${PENDING_RESUME_CONTEXT_PREFIX}\n\nOriginal request:\nNeed approval`,
    });

    expect(waiting).not.toBe(pending);
  });
});
