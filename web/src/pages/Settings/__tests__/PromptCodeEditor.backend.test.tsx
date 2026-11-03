import { act, render, screen, waitFor } from '@testing-library/react';
import { useState } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { forEachDiagnostic, type Diagnostic } from '@codemirror/lint';
import type { EditorView } from '@codemirror/view';
import PromptCodeEditor, {
  type PromptProfileContext,
} from '../profiles/promptEditor/PromptCodeEditor';
import { PROFILE_VARIABLES } from '../profiles/promptCompletions';
import { findEditor, settleCompletion, typeInEditor } from '../../../test/codemirror';
import type { PromptRefCheck } from '../../../types/schemas';
import type { PromptRefCheckRequest } from '../../../queries/promptRefs';

// #123: suggestions follow the profile's backend, and unknown references are
// underlined from the daemon's doctor check.

vi.mock('../../../queries/skills', () => ({
  useSkillsInventory: () => ({
    data: {
      ScanTime: '2026-10-14T00:00:00Z',
      Skills: [
        { Name: 'tdd', Provider: 'claude', Source: 'project', ApproxTokens: 1 },
        { Name: 'codex-lint', Provider: 'codex', Source: 'user-codex', ApproxTokens: 1 },
        { Name: 'shared-docs', Provider: 'shared', Source: 'user', ApproxTokens: 1 },
      ],
      Subagents: [
        { Name: 'code-reviewer', Provider: 'claude', Source: 'project', ApproxTokens: 1 },
      ],
      Prompts: [{ Name: 'review', Provider: 'codex', Source: 'user-codex', ApproxTokens: 1 }],
    },
  }),
}));

const checkPromptRefs = vi.fn<(req: PromptRefCheckRequest) => Promise<PromptRefCheck>>();
vi.mock('../../../queries/promptRefs', () => ({
  checkPromptRefs: (req: PromptRefCheckRequest) => checkPromptRefs(req),
}));

function Harness({ initial = '', profile }: { initial?: string; profile?: PromptProfileContext }) {
  const [value, setValue] = useState(initial);
  return (
    <PromptCodeEditor
      value={value}
      onChange={setValue}
      label="INSTRUCTIONS.md"
      variables={PROFILE_VARIABLES}
      profile={profile}
    />
  );
}

async function optionsAfter(view: EditorView, typed: string): Promise<string[]> {
  typeInEditor(view, typed);
  await screen.findByRole('listbox', { name: 'INSTRUCTIONS.md suggestions' });
  await settleCompletion();
  return screen.getAllByRole('option').map((o) => o.textContent);
}

function diagnostics(view: EditorView): Diagnostic[] {
  const out: Diagnostic[] = [];
  forEachDiagnostic(view.state, (d) => out.push(d));
  return out;
}

const claudeProfile: PromptProfileContext = {
  name: 'worker',
  command: 'claude',
  backend: 'claude',
};
const codexProfile: PromptProfileContext = { name: 'fast', command: 'codex', backend: 'codex' };

beforeEach(() => {
  checkPromptRefs.mockReset();
});

describe('PromptCodeEditor backend-aware suggestions (#123)', () => {
  it('Codex profile is not offered Claude subagents', async () => {
    checkPromptRefs.mockResolvedValue({ backend: 'codex', refs: [] });
    render(<Harness profile={codexProfile} />);
    const view = await findEditor('INSTRUCTIONS.md');
    const options = await optionsAfter(view, '/');
    expect(options).toEqual([
      'codex-lintskill · user-codex',
      'shared-docsskill · user',
      'prompts:reviewprompt · user-codex',
    ]);
    expect(options.join()).not.toContain('code-reviewer');
    expect(options.join()).not.toContain('tdd');
  });

  it('Claude profile is offered Claude subagents', async () => {
    checkPromptRefs.mockResolvedValue({ backend: 'claude', refs: [] });
    render(<Harness profile={claudeProfile} />);
    const view = await findEditor('INSTRUCTIONS.md');
    const options = await optionsAfter(view, '@');
    expect(options).toEqual([
      'tddskill · project',
      'shared-docsskill · user',
      'code-reviewersubagent · project',
    ]);
  });

  it('follows the backend the daemon resolves for the draft', async () => {
    // A wrapper command with a Claude selection that the daemon resolves to
    // Codex (e.g. the wrapper's binary is codex): the daemon wins.
    checkPromptRefs.mockResolvedValue({ backend: 'codex', refs: [] });
    render(<Harness profile={{ name: 'wrapped', command: 'run-agent.sh', backend: 'claude' }} />);
    const view = await findEditor('INSTRUCTIONS.md');
    await waitFor(() => {
      expect(checkPromptRefs).toHaveBeenCalledWith({
        profile: 'wrapped',
        command: 'run-agent.sh',
        backend: 'claude',
        text: '',
      });
    });
    const options = await optionsAfter(view, '@');
    expect(options.join()).not.toContain('code-reviewer');
  });

  it('without a profile, suggests every item and checks nothing', async () => {
    render(<Harness initial="Use the `ghost` skill." />);
    const view = await findEditor('INSTRUCTIONS.md');
    const options = await optionsAfter(view, ' /');
    expect(options).toHaveLength(5);
    await act(async () => {
      await new Promise((r) => setTimeout(r, 600));
    });
    expect(checkPromptRefs).not.toHaveBeenCalled();
    expect(diagnostics(view)).toEqual([]);
  });
});

describe('PromptCodeEditor reference underlines (#123)', () => {
  const text = 'Use the `tdd` skill and the `no-such-skill` skill.';
  const missingFrom = text.indexOf('no-such-skill');

  it('underlines a missing skill with a message and leaves a valid one alone', async () => {
    checkPromptRefs.mockResolvedValue({
      backend: 'claude',
      sshHosts: [],
      refs: [
        { kind: 'skill', name: 'tdd', from: 9, to: 12, status: 'ok' },
        {
          kind: 'skill',
          name: 'no-such-skill',
          from: missingFrom,
          to: missingFrom + 'no-such-skill'.length,
          status: 'missing',
          severity: 'warn',
          issueId: 'MISSING_SKILL_REF',
          message:
            'Profile "worker" (claude) references skill "no-such-skill", but no skill with that name was found.',
        },
      ],
    });
    const { container } = render(<Harness initial={text} profile={claudeProfile} />);
    const view = await findEditor('INSTRUCTIONS.md');

    await waitFor(
      () => {
        expect(container.querySelectorAll('.cm-lintRange-warning')).toHaveLength(1);
      },
      { timeout: 3000 },
    );
    const underlined = container.querySelector('.cm-lintRange-warning');
    expect(underlined).toHaveTextContent(/^no-such-skill$/);
    // The valid reference carries no lint mark at all.
    const marks = [...container.querySelectorAll('.cm-lintRange')].map((m) => m.textContent);
    expect(marks).toEqual(['no-such-skill']);

    const found = diagnostics(view);
    expect(found).toHaveLength(1);
    expect(found[0]).toMatchObject({
      from: missingFrom,
      to: missingFrom + 'no-such-skill'.length,
      severity: 'warning',
      message: expect.stringContaining('references skill "no-such-skill"') as unknown,
    });
    expect(checkPromptRefs).toHaveBeenCalledWith({
      profile: 'worker',
      command: 'claude',
      backend: 'claude',
      text,
    });
  });

  it('marks a reference that exists only outside the repo (SSH hosts) as info', async () => {
    checkPromptRefs.mockResolvedValue({
      backend: 'claude',
      sshHosts: ['build-1'],
      refs: [
        {
          kind: 'skill',
          name: 'tdd',
          from: 9,
          to: 12,
          status: 'outside_repo',
          severity: 'info',
          message: 'Agents on SSH hosts (build-1) see the repository but not this machine.',
        },
      ],
    });
    const { container } = render(<Harness initial={text} profile={claudeProfile} />);
    const view = await findEditor('INSTRUCTIONS.md');
    await waitFor(
      () => {
        expect(container.querySelector('.cm-lintRange-info')).toHaveTextContent('tdd');
      },
      { timeout: 3000 },
    );
    expect(diagnostics(view)[0].message).toContain('build-1');
  });

  it('clears the underlines when the check fails', async () => {
    checkPromptRefs.mockRejectedValue(new Error('offline'));
    const { container } = render(<Harness initial={text} profile={claudeProfile} />);
    await findEditor('INSTRUCTIONS.md');
    await waitFor(
      () => {
        expect(checkPromptRefs).toHaveBeenCalled();
      },
      { timeout: 3000 },
    );
    expect(container.querySelector('.cm-lintRange')).toBeNull();
  });
});
