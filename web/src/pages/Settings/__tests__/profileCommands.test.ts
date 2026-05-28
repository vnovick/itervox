import { describe, expect, it } from 'vitest';

import {
  applyBackendSelection,
  applyModelSelection,
  commandToBackend,
  draftFromProfileDef,
  isSimpleBackendCommand,
  normalizeCommandForSave,
} from '../profileCommands';

describe('profileCommands', () => {
  it('infers codex from absolute paths and env-prefixed commands', () => {
    expect(commandToBackend('/opt/tools/codex --model gpt-5.3-codex')).toBe('codex');
    expect(commandToBackend('OPENAI_API_KEY=x env /opt/tools/codex --model gpt-5.2-codex')).toBe(
      'codex',
    );
  });

  it('preserves custom wrapper commands when switching backend', () => {
    expect(applyBackendSelection('run-codex-wrapper --json', 'claude', 'codex')).toEqual({
      command: 'run-codex-wrapper --json',
      model: '',
    });
  });

  it('rewrites simple commands when switching backend', () => {
    expect(applyBackendSelection('claude --model claude-sonnet-4-6', 'claude', 'codex')).toEqual({
      command: 'codex',
      model: '',
    });
  });

  it('updates the model flag without flattening custom commands', () => {
    expect(
      applyModelSelection(
        'run-codex-wrapper --danger --model gpt-5.2-codex --json',
        'codex',
        'gpt-5.3-codex',
      ),
    ).toBe('run-codex-wrapper --danger --model gpt-5.3-codex --json');
  });

  it('normalizes blank commands back to the backend default', () => {
    expect(normalizeCommandForSave('   ', 'codex')).toBe('codex');
  });

  it('keeps wrapper commands and prompts intact when hydrating a profile', () => {
    expect(isSimpleBackendCommand('/usr/local/bin/codex --model gpt-5.3-codex', 'codex')).toBe(
      true,
    );
    expect(
      draftFromProfileDef({
        command: 'run-codex-wrapper --json',
        backend: 'codex',
        prompt: 'Investigate failures.',
        allowedActions: ['comment', 'provide_input'],
      }),
    ).toMatchObject({
      backend: 'codex',
      command: 'run-codex-wrapper --json',
      prompt: 'Investigate failures.',
      allowedActions: ['comment', 'provide_input'],
    });
  });

  it('hydrates file-backed profile text from soul and instructions', () => {
    expect(
      draftFromProfileDef({
        command: 'claude',
        soul: '# QA SOUL',
        instructions: '# QA INSTRUCTIONS',
        prompt: 'legacy fallback',
        soulFile: '.itervox/agents/qa/SOUL.md',
        instructionsFile: '.itervox/agents/qa/INSTRUCTIONS.md',
      }),
    ).toMatchObject({
      soul: '# QA SOUL',
      instructions: '# QA INSTRUCTIONS',
      prompt: '# QA INSTRUCTIONS',
      soulFile: '.itervox/agents/qa/SOUL.md',
      instructionsFile: '.itervox/agents/qa/INSTRUCTIONS.md',
    });
  });
});
