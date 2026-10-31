// Visual refresh — shared recipes added for the Timeline, Sessions, Logs,
// Agents and Automations pages: the console surface, template tiles, the
// quiet destructive button, the footer divider and the portalled modal.
import { describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Button } from '../button';
import { ConsoleSurface } from '../Console/ConsoleSurface';
import { Modal, ModalFooter } from '../modal';
import { TemplateTile, TileMeta } from '../../itervox/TemplateTile';

// node:fs is untyped under tsconfig.test.json; Vitest runs from web/.
const css: string = readFileSync(resolve(process.cwd(), 'src/index.css'), 'utf8');

describe('ConsoleSurface', () => {
  it('scopes the dark theme to its subtree and paints the console recipe', () => {
    render(
      <ConsoleSurface as="section" aria-label="Log" className="rounded-[26px]">
        <span>line</span>
      </ConsoleSurface>,
    );
    const el = screen.getByRole('region', { name: 'Log' });
    expect(el.tagName).toBe('SECTION');
    expect(el).toHaveAttribute('data-theme', 'dark');
    expect(el).toHaveClass('console-surface', 'rounded-[26px]');
  });

  it('defaults to a div', () => {
    const { container } = render(<ConsoleSurface>x</ConsoleSurface>);
    expect((container.firstChild as HTMLElement).tagName).toBe('DIV');
  });

  it('re-reads the colour utilities inside a nested data-theme subtree', () => {
    // The @theme mappings resolve once on :root; without this block the
    // text-theme-* utilities inside the console keep the page theme.
    const block = /\[data-theme\] \[data-theme\] \{([^}]*)\}/.exec(css)?.[1] ?? '';
    expect(block).toContain('--color-theme-text: var(--text);');
    expect(block).toContain('--color-theme-muted: var(--muted);');
    expect(css).toMatch(/\.console-surface \{[^}]*--terminal-bg: transparent;/);
  });
});

describe('TemplateTile', () => {
  it('is one button: title, meta pills, Use Template, description for assistive tech', async () => {
    const onUse = vi.fn();
    render(
      <TemplateTile
        title="QA Engineer"
        description="Writes test plans."
        tooltip="Actions: Comment"
        meta={<TileMeta mono>claude-sonnet</TileMeta>}
        onUse={onUse}
      />,
    );
    const tile = screen.getByRole('button', { name: /QA Engineer/ });
    expect(tile).toHaveClass('glass-tile');
    expect(tile).toHaveAccessibleName(/Writes test plans\./);
    expect(tile).toHaveAttribute('title', 'Writes test plans.\nActions: Comment');
    expect(screen.getByText('claude-sonnet')).toHaveClass('pill', 'pill-neutral', 'font-mono');
    expect(screen.getByText('Use Template')).toBeInTheDocument();
    await userEvent.click(tile);
    expect(onUse).toHaveBeenCalledTimes(1);
  });

  it('a disabled tile shows its note and does not fire', async () => {
    const onUse = vi.fn();
    render(<TemplateTile title="Gate" disabled note="Create the profile first." onUse={onUse} />);
    const tile = screen.getByRole('button', { name: /Gate/ });
    expect(tile).toBeDisabled();
    expect(tile).not.toHaveAttribute('title');
    // A quiet warning, not danger red: the tile is disabled, not failing.
    expect(screen.getByText('Create the profile first.')).toHaveClass('text-theme-warning-text');
    await userEvent.click(tile);
    expect(onUse).not.toHaveBeenCalled();
  });
});

describe('Button glass-danger', () => {
  it('is the glass chip with red text', () => {
    render(<Button variant="glass-danger">Delete</Button>);
    expect(screen.getByRole('button', { name: 'Delete' })).toHaveClass(
      'glass-chip',
      'text-theme-danger-text',
    );
  });
});

describe('ModalFooter divider', () => {
  it('draws a hairline only when asked', () => {
    const { rerender, container } = render(
      <ModalFooter>
        <button>OK</button>
      </ModalFooter>,
    );
    const footer = () => container.firstChild as HTMLElement;
    expect(footer()).not.toHaveAttribute('data-divider');
    expect(footer()).not.toHaveClass('border-t');
    rerender(
      <ModalFooter divider>
        <button>OK</button>
      </ModalFooter>,
    );
    expect(footer()).toHaveAttribute('data-divider', 'true');
    expect(footer()).toHaveClass('border-t', 'border-theme-line-strong');
  });

  it('the long-form (divider) footer pins to the bottom of the sheet; opt out with sticky={false}', () => {
    const { rerender, container } = render(
      <ModalFooter>
        <button>OK</button>
      </ModalFooter>,
    );
    const footer = () => container.firstChild as HTMLElement;
    expect(footer()).not.toHaveClass('sheet-footer');
    rerender(
      <ModalFooter divider>
        <button>OK</button>
      </ModalFooter>,
    );
    expect(footer()).toHaveClass('sheet-footer');
    expect(footer()).toHaveAttribute('data-sticky', 'true');
    rerender(
      <ModalFooter divider sticky={false}>
        <button>OK</button>
      </ModalFooter>,
    );
    expect(footer()).not.toHaveClass('sheet-footer');
    expect(css).toMatch(/\.sheet-footer \{[^}]*position: sticky;[^}]*bottom: 0;/);
  });
});

describe('Modal portal', () => {
  it('renders the dialog under <body>, outside an isolated ancestor', () => {
    const { container } = render(
      <div className="glass-panel" data-testid="panel">
        <Modal isOpen onClose={() => {}} ariaLabel="Portalled">
          <p>inside</p>
        </Modal>
      </div>,
    );
    const dialog = screen.getByRole('dialog', { name: 'Portalled' });
    expect(container.contains(dialog)).toBe(false);
    expect(dialog.closest('.modal')?.parentElement).toBe(document.body);
  });
});

describe('shared recipe classes', () => {
  // One definition per recipe: three page groups once each added their own
  // .stat-tile / .data-table / .choice-card. A second base rule for the same
  // class means two looks for one thing, so this fails it.
  it('defines each shared class base rule exactly once', () => {
    // Walk the rules with a brace stack so overrides inside @media /
    // @supports (theme fallbacks, breakpoints) are not counted, and only a
    // rule whose whole selector is the bare class is a base definition.
    const counts = new Map<string, number>();
    const stack: string[] = [];
    const src = css.replace(/\/\*[\s\S]*?\*\//g, '');
    let prelude = '';
    for (const ch of src) {
      if (ch === '{') {
        const sel = prelude.trim();
        const conditional = stack.some((s) => /^@(media|supports)/.test(s));
        const bare = /^\.([a-z][a-z0-9-]*)$/.exec(sel);
        if (bare && !conditional) counts.set(bare[1], (counts.get(bare[1]) ?? 0) + 1);
        stack.push(sel);
        prelude = '';
      } else if (ch === '}') {
        stack.pop();
        prelude = '';
      } else if (ch === ';') {
        prelude = '';
      } else {
        prelude += ch;
      }
    }
    const duplicated = [...counts].filter(([, n]) => n > 1).map(([name]) => name);
    expect(duplicated).toEqual([]);
    // And each shared recipe has its base rule outside any @media block, so
    // it applies to everyone, not only under a fallback preference.
    const required = [
      'glass-card',
      'glass-chip',
      'glass-sheet',
      'pill',
      'count-pill',
      'stat-tile',
      'data-table',
      'board-column',
      'kv-grid',
      'code-line',
      'code-block',
      'choice-card',
      'tag-chip',
      'switch',
      'field-label',
      'field-help',
      'field-group',
      'list-row',
      'glass-tile',
      'console-surface',
    ];
    expect(required.filter((name) => counts.get(name) !== 1)).toEqual([]);
  });

  it.each([
    'field-label',
    'field-help',
    'field-group',
    'list-row',
    'glass-tile',
    'stat-tile-label',
    'stat-tile-value',
    'console-bar',
  ])('index.css defines .%s', (name) => {
    expect(css).toMatch(new RegExp(`\\.${name} \\{`));
  });

  it('.list-row highlights the current / selected row with the card recipe', () => {
    const rule = /\.list-row:is\(([^)]*)\)\s*\{([^}]*)\}/.exec(css);
    expect(rule?.[1]).toContain("[aria-current='true']");
    expect(rule?.[1]).toContain("[aria-selected='true']");
    expect(rule?.[2]).toContain('background: var(--card)');
  });
});
