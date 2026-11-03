import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { ensureSyntaxTree } from '@codemirror/language';
import { MarkdownPromptEditor } from '../profiles/MarkdownPromptEditor';
import { findEditor, moveCaret, replaceEditorText, typeInEditor } from '../../../test/codemirror';

vi.mock('../../../queries/skills', () => ({
  useSkillsInventory: () => ({ data: null }),
}));

// Markdown, Liquid objects and tags, unicode, tabs, trailing spaces, blank
// lines and a fenced block: everything a prompt file carries.
const PROMPT = [
  '# Identity',
  '',
  'You work on {{ issue.identifier }} — “{{ issue.title | downcase }}”.  ',
  '{% if attempt %}Retry {{ attempt }}{% endif %}',
  '',
  '\t- keep `go test -race` green',
  '```bash',
  'make verify',
  '```',
  '',
].join('\n');

function Harness({ initial, onValue }: { initial: string; onValue?: (v: string) => void }) {
  const [value, setValue] = useState(initial);
  return (
    <>
      <MarkdownPromptEditor
        label="SOUL.md"
        value={value}
        onChange={(v) => {
          setValue(v);
          onValue?.(v);
        }}
      />
      <button
        type="button"
        onClick={() => {
          setValue('# Reset');
        }}
      >
        reset
      </button>
    </>
  );
}

describe('MarkdownPromptEditor on CodeMirror (#122)', () => {
  it('loads the editor lazily, showing the text read-only meanwhile', async () => {
    render(<Harness initial={PROMPT} />);
    const fallback = screen.getByRole('textbox', { name: 'SOUL.md (loading editor)' });
    expect(fallback).toHaveAttribute('readonly');
    expect(fallback).toHaveValue(PROMPT);
    const view = await findEditor('SOUL.md');
    expect(view.state.doc.toString()).toBe(PROMPT);
    expect(
      screen.queryByRole('textbox', { name: 'SOUL.md (loading editor)' }),
    ).not.toBeInTheDocument();
  });

  it('round-trips the text unchanged: untouched, edited, and across Preview', async () => {
    const user = userEvent.setup();
    const onValue = vi.fn();
    render(<Harness initial={PROMPT} onValue={onValue} />);
    let view = await findEditor('SOUL.md');
    expect(onValue).not.toHaveBeenCalled(); // opening never rewrites the text

    replaceEditorText(view, PROMPT + 'Done.');
    expect(onValue).toHaveBeenLastCalledWith(PROMPT + 'Done.');

    await user.click(screen.getByRole('button', { name: 'Preview' }));
    expect(screen.getByRole('heading', { name: 'Identity' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Write' }));
    view = await findEditor('SOUL.md');
    expect(view.state.doc.toString()).toBe(PROMPT + 'Done.');
    expect(onValue).toHaveBeenCalledTimes(1);
  });

  it('takes a value set from outside without echoing it back as an edit', async () => {
    const user = userEvent.setup();
    const onValue = vi.fn();
    render(<Harness initial={PROMPT} onValue={onValue} />);
    const view = await findEditor('SOUL.md');
    await user.click(screen.getByRole('button', { name: 'reset' }));
    expect(view.state.doc.toString()).toBe('# Reset');
    expect(onValue).not.toHaveBeenCalled();
    moveCaret(view, view.state.doc.length);
    typeInEditor(view, '!');
    expect(onValue).toHaveBeenLastCalledWith('# Reset!');
  });

  it('highlights Liquid objects and tags, Markdown headings and code', async () => {
    render(<Harness initial={PROMPT} />);
    const view = await findEditor('SOUL.md');
    ensureSyntaxTree(view.state, view.state.doc.length, 5000);
    await waitFor(() => {
      const texts = (cls: string) =>
        Array.from(view.contentDOM.querySelectorAll(`.${cls}`)).map((el) => el.textContent);
      expect(texts('cm-liquid-object')).toEqual([
        '{{ issue.identifier }}',
        '{{ issue.title | downcase }}',
        '{{ attempt }}',
      ]);
      expect(texts('cm-liquid-tag')).toEqual(['{% if attempt %}', '{% endif %}']);
      expect(texts('cm-md-heading').join('')).toBe('# Identity');
      expect(texts('cm-md-code').join('')).toContain('go test -race');
      expect(texts('cm-md-code').join('')).toContain('make verify');
    });
  });
});
