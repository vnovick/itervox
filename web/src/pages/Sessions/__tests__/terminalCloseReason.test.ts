import { describe, expect, it } from 'vitest';
import { TERMINAL_CLOSE_ACCESS_LOST, terminalCloseMessage } from '../terminalCloseReason';

describe('terminalCloseMessage', () => {
  it('explains each access-lost reason the daemon sends', () => {
    for (const reason of [
      'token_revoked',
      'token_expired',
      'token_rotated',
      'scope_insufficient',
      'token_store_invalid',
      // through the hub: the caller's hub token, or the hub's member token
      'hub_token_revoked',
      'hub_token_expired',
      'hub_token_rotated',
      'hub_scope_insufficient',
      'hub_token_store_invalid',
      'member_token_revoked',
      'member_token_expired',
      'member_token_rotated',
      'member_scope_insufficient',
      'member_token_store_invalid',
    ]) {
      const text = terminalCloseMessage(TERMINAL_CLOSE_ACCESS_LOST, reason);
      expect(text).not.toBe('');
      expect(text).not.toContain(reason);
    }
  });

  it('names an unknown reason rather than hiding it', () => {
    expect(terminalCloseMessage(TERMINAL_CLOSE_ACCESS_LOST, 'something_new')).toContain(
      'something_new',
    );
    expect(terminalCloseMessage(TERMINAL_CLOSE_ACCESS_LOST, '')).toContain('access changed');
  });

  it('is empty for an ordinary close', () => {
    expect(terminalCloseMessage(1000, '')).toBe('');
    expect(terminalCloseMessage(1006, 'token_revoked')).toBe('');
  });
});
