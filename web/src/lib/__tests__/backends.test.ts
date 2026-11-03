import { describe, expect, it } from 'vitest';
import {
  AGENT_BACKENDS,
  backendForBinary,
  backendFromHint,
  backendLabel,
  isAgentBackend,
  resolveIssueBackend,
} from '../backends';
import type { ProfileDef } from '../../types/schemas';

describe('backends', () => {
  it('lists the backends the daemon accepts, in order', () => {
    expect(AGENT_BACKENDS).toEqual(['claude', 'codex', 'opencode', 'cursor', 'custom']);
    expect(isAgentBackend('cursor')).toBe(true);
    expect(isAgentBackend('Cursor')).toBe(false);
    expect(isAgentBackend(undefined)).toBe(false);
  });

  it('labels backends and passes unknown values through', () => {
    expect(backendLabel('opencode')).toBe('OpenCode');
    expect(backendLabel('custom')).toBe('Custom');
    expect(backendLabel('gemini')).toBe('gemini');
  });

  it('maps CLI binaries to backends (custom has none)', () => {
    expect(backendForBinary('cursor-agent')).toBe('cursor');
    expect(backendForBinary('opencode')).toBe('opencode');
    expect(backendForBinary('cursor')).toBeNull();
    expect(backendForBinary('')).toBeNull();
  });

  it('reads a backend from a name or a command', () => {
    expect(backendFromHint('custom')).toBe('custom');
    expect(backendFromHint('/opt/bin/cursor-agent --model x')).toBe('cursor');
    expect(backendFromHint('opencode --model anthropic/claude-sonnet')).toBe('opencode');
    expect(backendFromHint('claude --model opus')).toBe('claude');
    expect(backendFromHint('codex exec')).toBe('codex');
    expect(backendFromHint('./wrapper.sh')).toBeNull();
    expect(backendFromHint('')).toBeNull();
  });

  it('resolves an issue backend: running, then profile, then default', () => {
    const defs = {
      oc: { command: 'opencode' },
      wrap: { command: './wrap.sh', backend: 'custom' },
      opaque: { command: './wrap.sh' },
    } as unknown as Record<string, ProfileDef>;
    expect(resolveIssueBackend('oc', defs, 'cursor', 'claude')).toBe('cursor');
    expect(resolveIssueBackend('oc', defs, undefined, 'claude')).toBe('opencode');
    expect(resolveIssueBackend('wrap', defs, undefined, 'claude')).toBe('custom');
    expect(resolveIssueBackend('opaque', defs, undefined, 'codex')).toBe('claude');
    expect(resolveIssueBackend(undefined, defs, undefined, 'codex')).toBe('codex');
    expect(resolveIssueBackend(undefined, defs, undefined, undefined)).toBe('claude');
  });
});
