import { fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { PromptAutocompleteTextarea } from '../profiles/PromptAutocompleteTextarea';
import { PROFILE_VARIABLES } from '../profiles/promptCompletions';
import { removeLayer, pushLayer } from '../../../components/ui/dialog/overlayStack';

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

function Harness({ initial = '', onEscape }: { initial?: string; onEscape?: () => void }) {
  const [value, setValue] = useState(initial);
  return (
    // The surrounding dialog's Escape handler.
    // eslint-disable-next-line jsx-a11y/no-static-element-interactions
    <div
      onKeyDown={(e) => {
        if (e.key === 'Escape') onEscape?.();
      }}
    >
      <PromptAutocompleteTextarea
        value={value}
        onChange={setValue}
        label="INSTRUCTIONS.md"
        variables={PROFILE_VARIABLES}
      />
    </div>
  );
}

function typeText(el: HTMLTextAreaElement, text: string) {
  fireEvent.change(el, { target: { value: text, selectionStart: text.length } });
  el.setSelectionRange(text.length, text.length);
}

describe('PromptAutocompleteTextarea (#87)', () => {
  it('lists skills and subagents after / and inserts the chosen name with Enter', () => {
    render(<Harness />);
    const box = screen.getByRole('textbox', { name: 'INSTRUCTIONS.md' });
    typeText(box, 'Use /');
    const list = screen.getByRole('listbox', { name: 'INSTRUCTIONS.md suggestions' });
    const options = screen.getAllByRole('option');
    expect(options.map((o) => o.textContent)).toEqual([
      'tddskill · projectTest first',
      'deployskill · user',
      'code-reviewersubagent · project' + 'Reviews diffs',
    ]);
    expect(box).toHaveAttribute('aria-controls', list.id);
    expect(box).toHaveAttribute('aria-activedescendant', options[0].id);
    expect(options[0]).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('status')).toHaveTextContent('3 suggestions');

    fireEvent.keyDown(box, { key: 'ArrowDown' });
    fireEvent.keyDown(box, { key: 'ArrowDown' });
    expect(box).toHaveAttribute('aria-activedescendant', options[2].id);
    fireEvent.keyDown(box, { key: 'ArrowDown' }); // wraps around
    fireEvent.keyDown(box, { key: 'ArrowUp' }); // and back
    fireEvent.keyDown(box, { key: 'Enter' });
    expect(box.value).toBe('Use /code-reviewer ');
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
  });

  it('filters by the typed text and lists subagents after @', () => {
    render(<Harness />);
    const box = screen.getByRole('textbox');
    typeText(box, '@code');
    expect(screen.getAllByRole('option')).toHaveLength(1);
    fireEvent.keyDown(box, { key: 'Tab' });
    expect(box.value).toBe('@code-reviewer ');
  });

  it('lists Liquid variables after {{ and closes the tag on insert', () => {
    render(<Harness />);
    const box = screen.getByRole('textbox');
    typeText(box, 'Title: {{ issue.ti');
    expect(screen.getAllByRole('option')[0]).toHaveTextContent('issue.title');
    fireEvent.mouseDown(screen.getAllByRole('option')[0]);
    expect(box.value).toBe('Title: {{ issue.title }}');
  });

  it('closes on Escape without letting it reach the surrounding dialog', () => {
    const onEscape = vi.fn();
    render(<Harness onEscape={onEscape} />);
    const box = screen.getByRole('textbox');
    typeText(box, '{{');
    expect(screen.getByRole('listbox')).toBeInTheDocument();
    fireEvent.keyDown(box, { key: 'Escape' });
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
    expect(onEscape).not.toHaveBeenCalled();
    fireEvent.keyDown(box, { key: 'Escape' }); // with the list closed, Escape is the dialog's
    expect(onEscape).toHaveBeenCalledTimes(1);
  });

  it('shows nothing when no suggestion matches, and Enter types a newline', () => {
    render(<Harness />);
    const box = screen.getByRole('textbox');
    typeText(box, '/zzz');
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
    expect(box).not.toHaveAttribute('aria-activedescendant');
    const enter = fireEvent.keyDown(box, { key: 'Enter' });
    expect(enter).toBe(true); // not prevented
  });

  it('keeps the first Escape from the real dialog layer', () => {
    const onEscape = vi.fn();
    pushLayer('profile-dialog', onEscape);
    try {
      render(<Harness />);
      const box = screen.getByRole('textbox');
      typeText(box, '/');
      fireEvent.keyDown(box, { key: 'Escape' });
      expect(onEscape).not.toHaveBeenCalled();
      fireEvent.keyDown(box, { key: 'Escape' });
      expect(onEscape).toHaveBeenCalledTimes(1);
    } finally {
      removeLayer('profile-dialog');
    }
  });
});
