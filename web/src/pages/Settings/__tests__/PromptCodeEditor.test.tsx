import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import type { EditorView } from '@codemirror/view';
import PromptCodeEditor from '../profiles/promptEditor/PromptCodeEditor';
import { PROFILE_VARIABLES } from '../profiles/promptCompletions';
import { removeLayer, pushLayer } from '../../../components/ui/dialog/overlayStack';
import { findEditor, moveCaret, settleCompletion, typeInEditor } from '../../../test/codemirror';

vi.mock('../../../queries/skills', () => ({
  useSkillsInventory: () => ({
    data: {
      ScanTime: '2026-10-09T00:00:00Z',
      Skills: [
        {
          Name: 'tdd',
          Description: 'Test first',
          Provider: 'claude',
          Source: 'project',
          ApproxTokens: 1,
        },
        { Name: 'deploy', Provider: 'claude', Source: 'user', ApproxTokens: 1 },
      ],
      Subagents: [
        {
          Name: 'code-reviewer',
          Description: 'Reviews diffs',
          Provider: 'claude',
          Source: 'project',
          ApproxTokens: 1,
        },
      ],
    },
  }),
}));

function Harness({
  initial = '',
  onEscape,
  onValue,
}: {
  initial?: string;
  onEscape?: () => void;
  onValue?: (v: string) => void;
}) {
  const [value, setValue] = useState(initial);
  return (
    // The surrounding dialog's Escape handler.
    // eslint-disable-next-line jsx-a11y/no-static-element-interactions
    <div
      onKeyDown={(e) => {
        if (e.key === 'Escape') onEscape?.();
      }}
    >
      <PromptCodeEditor
        value={value}
        onChange={(v) => {
          setValue(v);
          onValue?.(v);
        }}
        label="INSTRUCTIONS.md"
        variables={PROFILE_VARIABLES}
      />
    </div>
  );
}

const content = (view: EditorView) => view.contentDOM;
const text = (view: EditorView) => view.state.doc.toString();
const key = (view: EditorView, k: string, init: KeyboardEventInit = {}) =>
  fireEvent.keyDown(content(view), { key: k, ...init });

async function openList(view: EditorView, typed: string) {
  typeInEditor(view, typed);
  const list = await screen.findByRole('listbox', { name: 'INSTRUCTIONS.md suggestions' });
  await settleCompletion();
  return list;
}

describe('PromptCodeEditor suggestions (#87 on CodeMirror, #122)', () => {
  it('lists skills and subagents after / and inserts the chosen name with Enter', async () => {
    render(<Harness />);
    const view = await findEditor('INSTRUCTIONS.md');
    const list = await openList(view, 'Use /');
    const options = screen.getAllByRole('option');
    expect(options.map((o) => o.textContent)).toEqual([
      'tddskill · project',
      'deployskill · user',
      'code-reviewersubagent · project',
    ]);
    expect(content(view)).toHaveAttribute('aria-controls', list.id);
    expect(content(view)).toHaveAttribute('aria-activedescendant', options[0].id);
    expect(options[0]).toHaveAttribute('aria-selected', 'true');
    await waitFor(() => {
      expect(screen.getByRole('status')).toHaveTextContent('3 suggestions');
    });

    key(view, 'ArrowDown');
    key(view, 'ArrowDown');
    await waitFor(() => {
      expect(content(view)).toHaveAttribute(
        'aria-activedescendant',
        screen.getAllByRole('option')[2].id,
      );
    });
    key(view, 'ArrowDown'); // wraps around
    key(view, 'ArrowUp'); // and back
    key(view, 'Enter');
    expect(text(view)).toBe('Use /code-reviewer ');
    await waitFor(() => {
      expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
    });
  });

  it('filters by the typed text and lists subagents after @', async () => {
    const onValue = vi.fn();
    render(<Harness onValue={onValue} />);
    const view = await findEditor('INSTRUCTIONS.md');
    await openList(view, '@code');
    expect(screen.getAllByRole('option')).toHaveLength(1);
    key(view, 'Enter');
    expect(text(view)).toBe('@code-reviewer ');
    expect(onValue).toHaveBeenLastCalledWith('@code-reviewer ');
  });

  it('lists Liquid variables after {{ and closes the tag on insert', async () => {
    render(<Harness />);
    const view = await findEditor('INSTRUCTIONS.md');
    await openList(view, 'Title: {{ issue.ti');
    const first = screen.getAllByRole('option')[0];
    expect(first).toHaveTextContent('issue.title');
    fireEvent.mouseDown(first);
    expect(text(view)).toBe('Title: {{ issue.title }}');
  });

  it('suggests filters after | inside {{ }} and keeps the closer', async () => {
    render(<Harness initial="{{ issue.title  }}" />);
    const view = await findEditor('INSTRUCTIONS.md');
    moveCaret(view, 15);
    await openList(view, '| up');
    expect(screen.getAllByRole('option')[0]).toHaveTextContent('upcase');
    key(view, 'Enter');
    expect(text(view)).toBe('{{ issue.title | upcase }}');
  });

  it('closes on Escape without letting it reach the surrounding dialog', async () => {
    const onEscape = vi.fn();
    render(<Harness onEscape={onEscape} />);
    const view = await findEditor('INSTRUCTIONS.md');
    await openList(view, '{{');
    key(view, 'Escape');
    await waitFor(() => {
      expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
    });
    expect(onEscape).not.toHaveBeenCalled();
    key(view, 'Escape'); // with the list closed, Escape is the dialog's
    expect(onEscape).toHaveBeenCalledTimes(1);
  });

  it('shows nothing when no suggestion matches, and Enter types a newline', async () => {
    render(<Harness />);
    const view = await findEditor('INSTRUCTIONS.md');
    typeInEditor(view, '/zzz');
    await settleCompletion();
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
    expect(content(view)).not.toHaveAttribute('aria-activedescendant');
    key(view, 'Enter');
    expect(text(view)).toBe('/zzz\n');
  });

  it('keeps the first Escape from the real dialog layer', async () => {
    const onEscape = vi.fn();
    pushLayer('profile-dialog', onEscape);
    try {
      render(<Harness />);
      const view = await findEditor('INSTRUCTIONS.md');
      await openList(view, '/');
      key(view, 'Escape');
      expect(onEscape).not.toHaveBeenCalled();
      key(view, 'Escape');
      expect(onEscape).toHaveBeenCalledTimes(1);
    } finally {
      removeLayer('profile-dialog');
    }
  });

  it('never inserts at a caret that moved after the list opened', async () => {
    render(<Harness />);
    const view = await findEditor('INSTRUCTIONS.md');
    await openList(view, 'Use /td');
    moveCaret(view, 4); // before the `/`, as ArrowLeft would
    await waitFor(() => {
      expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
    });
    key(view, 'Enter'); // a newline, not an insert
    expect(text(view)).toBe('Use \n/td');
  });

  it('leaves Tab to focus navigation', async () => {
    render(<Harness />);
    const view = await findEditor('INSTRUCTIONS.md');
    await openList(view, '/td');
    expect(key(view, 'Tab')).toBe(true); // not prevented
    expect(text(view)).toBe('/td');
    expect(screen.getByRole('listbox')).toBeInTheDocument();
  });
});
