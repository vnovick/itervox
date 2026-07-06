import { describe, expect, it } from 'vitest';
import { StateSnapshotSchema, TrackerIssueSchema } from '../../../types/schemas';
import {
  activeRunScenario,
  configInvalidScenario,
  emptyScenario,
  inputRequiredScenario,
  mobileShellScenario,
  quickstartScenario,
  retryAndPausedScenario,
  settingsMatrixScenario,
  timelineLogsScenario,
} from '../scenarios';

const ALL_SCENARIOS = {
  emptyScenario,
  quickstartScenario,
  activeRunScenario,
  inputRequiredScenario,
  retryAndPausedScenario,
  configInvalidScenario,
  timelineLogsScenario,
  settingsMatrixScenario,
  mobileShellScenario,
};

describe('scenarios', () => {
  it('every scenario has a schema-valid snapshot', () => {
    for (const [name, scenario] of Object.entries(ALL_SCENARIOS)) {
      expect(() => StateSnapshotSchema.parse(scenario.snapshot), name).not.toThrow();
    }
  });

  it('every issue in every scenario is schema-valid', () => {
    for (const [name, scenario] of Object.entries(ALL_SCENARIOS)) {
      for (const issue of scenario.issues) {
        expect(() => TrackerIssueSchema.parse(issue), `${name}:${issue.identifier}`).not.toThrow();
      }
    }
  });

  it('emptyScenario has all empty arrays', () => {
    expect(emptyScenario.snapshot.running).toEqual([]);
    expect(emptyScenario.snapshot.retrying).toEqual([]);
    expect(emptyScenario.snapshot.paused).toEqual([]);
    expect(emptyScenario.snapshot.history).toEqual([]);
    expect(emptyScenario.snapshot.inputRequired).toEqual([]);
    expect(emptyScenario.issues).toEqual([]);
  });

  it('inputRequiredScenario has both input_required and pending_input_resume rows', () => {
    const states = inputRequiredScenario.snapshot.inputRequired?.map((r) => r.state).sort();
    expect(states).toEqual(['input_required', 'pending_input_resume']);
  });

  it('configInvalidScenario surfaces a populated configInvalid', () => {
    expect(configInvalidScenario.snapshot.configInvalid).toBeDefined();
    expect(configInvalidScenario.snapshot.configInvalid?.error).toBeTruthy();
  });

  it('timelineLogsScenario covers every history status', () => {
    const statuses = timelineLogsScenario.snapshot.history?.map((h) => h.status);
    expect(new Set(statuses)).toEqual(
      new Set(['succeeded', 'failed', 'cancelled', 'stalled', 'input_required']),
    );
  });

  it('settingsMatrixScenario exposes profiles, automations, and SSH hosts', () => {
    const snap = settingsMatrixScenario.snapshot;
    expect(snap.availableProfiles?.length).toBeGreaterThanOrEqual(2);
    expect(snap.automations?.length).toBeGreaterThanOrEqual(2);
    expect(snap.sshHosts?.length).toBeGreaterThanOrEqual(2);
  });

  it('activeRunScenario has at least one running row', () => {
    expect(activeRunScenario.snapshot.running.length).toBeGreaterThan(0);
    expect(activeRunScenario.snapshot.counts.running).toBe(
      activeRunScenario.snapshot.running.length,
    );
  });

  it('retryAndPausedScenario carries retry rows, paused identifiers, and a pausedWithPR mapping', () => {
    expect(retryAndPausedScenario.snapshot.retrying.length).toBeGreaterThan(0);
    expect(retryAndPausedScenario.snapshot.paused.length).toBeGreaterThan(0);
    expect(retryAndPausedScenario.snapshot.pausedWithPR).toBeDefined();
  });
});
