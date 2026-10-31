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
    expect(screen.getByText('Create the profile first.')).toHaveClass('text-theme-danger-text');
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
  it.each([
    'field-label',
    'field-help',
    'field-group',
    'list-row',
    'glass-tile',
    'metric-label',
    'metric-value',
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
