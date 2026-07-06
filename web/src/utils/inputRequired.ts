import type { StateSnapshot } from '../types/schemas';

type SnapshotInputRequiredRow = NonNullable<StateSnapshot['inputRequired']>[number];
type InputRequiredRowStateInput = {
  state?: SnapshotInputRequiredRow['state'];
  context?: SnapshotInputRequiredRow['context'];
};
type InputRequiredFingerprintInput = InputRequiredRowStateInput & {
  identifier: SnapshotInputRequiredRow['identifier'];
};

export const PENDING_RESUME_CONTEXT_PREFIX = 'Reply received, waiting to resume.';

export function inputRequiredRowState(
  entry: InputRequiredRowStateInput,
): 'input_required' | 'pending_input_resume' {
  if (entry.state === 'pending_input_resume' || entry.state === 'input_required') {
    return entry.state;
  }
  return (entry.context || '').startsWith(PENDING_RESUME_CONTEXT_PREFIX)
    ? 'pending_input_resume'
    : 'input_required';
}

export function inputRequiredFingerprintValue(entry: InputRequiredFingerprintInput): string {
  return `${entry.identifier}:${inputRequiredRowState(entry)}`;
}
