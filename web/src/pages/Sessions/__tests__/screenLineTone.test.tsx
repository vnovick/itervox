import { describe, expect, it, vi } from 'vitest';
import { render } from '@testing-library/react';
import { TmuxScreen } from '../TmuxSessionView';
import { screenLineTone } from '../screenLineTone';

vi.mock('../../../queries/sessionsTmux', () => ({
  useSessionScreen: () => ({
    data: {
      text: '> Tidy the fixtures\n● Reading a.ts\n✓ Edited a.ts\n✗ Tests failed\n? Keep the old ids?\nWhat? not a question line',
    },
    isError: false,
  }),
}));

describe('tmux screen semantic tints', () => {
  it('classifies only lines whose leading glyph is known', () => {
    expect(screenLineTone('✓ Edited src/a.ts')).toBe('ok');
    expect(screenLineTone('  ✔ done')).toBe('ok');
    expect(screenLineTone('✗ go test failed')).toBe('danger');
    expect(screenLineTone('? Keep the old ids?')).toBe('warn');
    expect(screenLineTone('● Reading src/a.ts')).toBeNull();
    expect(screenLineTone('?? untracked.txt')).toBeNull();
    expect(screenLineTone('What? plain prose')).toBeNull();
    expect(screenLineTone('')).toBeNull();
  });

  it('tints the edited line ok and the question warn, keeping the pane text intact', () => {
    const { getByTestId } = render(<TmuxScreen id="s1" />);
    const pre = getByTestId('tmux-screen');
    expect(pre.textContent).toBe(
      '> Tidy the fixtures\n● Reading a.ts\n✓ Edited a.ts\n✗ Tests failed\n? Keep the old ids?\nWhat? not a question line',
    );
    const toned = [...pre.querySelectorAll('[data-tone]')].map((el) => [
      el.getAttribute('data-tone'),
      el.className,
      el.textContent.trim(),
    ]);
    expect(toned).toEqual([
      ['ok', 'text-theme-success-text', '✓ Edited a.ts'],
      ['danger', 'text-theme-danger-text', '✗ Tests failed'],
      ['warn', 'text-theme-warning-text', '? Keep the old ids?'],
    ]);
  });
});
