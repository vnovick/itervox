import { describe, expect, it } from 'vitest';
import { findingSeverity, laneReasonKey, laneReasonLocation } from '../laneReasons';

const finding = (rule: string) => ({ kind: 'finding', path: 'a.yml', line: 3, rule });

describe('laneReasons', () => {
  it('ranks rules that switch a check off or hide its failure as high', () => {
    for (const rule of [
      'ci_continue_on_error',
      'ci_or_true',
      'test_deleted',
      'git_no_verify',
      'coverage_lowered',
      'js_only',
      // #336's rules: harmless until the daemon sends them.
      'ts_strict_off',
      'script_stubbed',
      'check_or_true',
      'snapshot_update',
      'test_pass_with_no_tests',
      'eslint_rule_off',
    ]) {
      expect(findingSeverity(finding(rule)), rule).toBe('high');
    }
  });

  it('ranks a skip, or no rule at all, as medium', () => {
    expect(findingSeverity(finding('go_skip'))).toBe('medium');
    expect(findingSeverity({ kind: 'finding', path: 'a.yml' })).toBe('medium');
  });

  it('locates and keys a reason', () => {
    expect(laneReasonLocation(finding('x'))).toBe('a.yml:3');
    expect(laneReasonLocation({ kind: 'lane_rule', path: 'a.yml', line: 0 })).toBe('a.yml');
    expect(laneReasonKey(finding('x'))).toBe('finding:a.yml:3:x');
  });
});
