// Visual refresh — the shared recipes added for the dashboard: SlidePanel
// widths and mono title, ConfirmButton sizes, the merge-free button recipe,
// the operator-queue tone dots and the shared CSS classes in index.css.
import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { SlidePanel } from '../SlidePanel/SlidePanel';
import { ConfirmButton } from '../button/ConfirmButton';
import { BUTTON_SIZE, BUTTON_VARIANT, plainButtonClass } from '../button/buttonRecipe';
import { buttonClass } from '../button/buttonStyles';
import { FIELD_CLASS } from '../Input/fieldClass';
import { FIELD_CLASS as FIELD_CLASS_FROM_INPUT } from '../Input';
import { operatorToneDotClass } from '../../../lib/operatorQueue';

describe('SlidePanel size and monoTitle', () => {
  const open = (props: Partial<Parameters<typeof SlidePanel>[0]> = {}) =>
    render(
      <SlidePanel isOpen onClose={vi.fn()} title="ENG-1" {...props}>
        <p>x</p>
      </SlidePanel>,
    );

  it('is 75vw wide by default and keeps the sans title', () => {
    open();
    expect(screen.getByRole('dialog')).toHaveClass('md:w-[75vw]');
    expect(screen.getByRole('heading', { name: 'ENG-1' })).not.toHaveClass('font-mono');
  });

  it('medium and narrow cap the width', () => {
    const { unmount } = open({ size: 'medium' });
    expect(screen.getByRole('dialog')).toHaveClass('md:w-[min(820px,75vw)]');
    expect(screen.getByRole('dialog')).not.toHaveClass('md:w-[75vw]');
    unmount();
    open({ size: 'narrow', direction: 'left' });
    expect(screen.getByRole('dialog')).toHaveClass('md:w-[min(520px,75vw)]');
  });

  it('a bottom sheet ignores the width', () => {
    open({ size: 'narrow', direction: 'bottom' });
    expect(screen.getByRole('dialog').className).not.toMatch(/md:w-/);
  });

  it('monoTitle sets the identifier in the mono face', () => {
    open({ monoTitle: true });
    expect(screen.getByRole('heading', { name: 'ENG-1' })).toHaveClass('font-mono');
  });
});

describe('ConfirmButton size', () => {
  const button = (size?: 'sm' | 'lg') => (
    <ConfirmButton
      label="Discard"
      confirmLabel="Yes"
      pendingLabel="…"
      isPending={false}
      onConfirm={vi.fn()}
      size={size}
    />
  );

  it('is a small row trigger by default', () => {
    render(button());
    expect(screen.getByRole('button', { name: 'Discard' })).toHaveClass('text-xs', 'py-1.5');
  });

  it('lg matches the 44px footer buttons', () => {
    render(button('lg'));
    const trigger = screen.getByRole('button', { name: 'Discard' });
    expect(trigger).toHaveClass('min-h-11', 'text-[15px]');
    expect(trigger).not.toHaveClass('text-xs');
  });
});

describe('plainButtonClass (no tailwind-merge)', () => {
  it('composes the same recipe as buttonClass', () => {
    const plain = plainButtonClass('primary', 'md').split(' ');
    for (const cls of [...BUTTON_VARIANT.primary.split(' '), ...BUTTON_SIZE.md.split(' ')]) {
      expect(plain).toContain(cls);
    }
    expect(plain).toContain('rounded-[var(--radius-sm)]');
    const merged = buttonClass('primary', 'md').split(' ');
    expect(new Set(plain)).toEqual(new Set(merged));
  });

  it('pill swaps the radius and extra classes are appended', () => {
    const cls = plainButtonClass('secondary', 'sm', { pill: true, extra: 'ml-auto' }).split(' ');
    expect(cls).toContain('rounded-full');
    expect(cls).not.toContain('rounded-[var(--radius-sm)]');
    expect(cls).toContain('glass-chip');
    expect(cls[cls.length - 1]).toBe('ml-auto');
  });

  it('FIELD_CLASS is the same recipe from both modules', () => {
    expect(FIELD_CLASS_FROM_INPUT).toBe(FIELD_CLASS);
  });
});

describe('operatorToneDotClass', () => {
  it('maps each operator-queue tone to a status-dot colour', () => {
    expect(operatorToneDotClass('danger')).toBe('dot-danger');
    expect(operatorToneDotClass('warning')).toBe('dot-warn');
    expect(operatorToneDotClass('info')).toBe('bg-theme-accent');
  });
});

describe('shared CSS classes (index.css)', () => {
  // Vitest runs from web/ (same convention as tokens.contrast.test.ts).
  const css: string = readFileSync(resolve(process.cwd(), 'src/index.css'), 'utf8');
  // The rules live in the components layer, after the base recipes.
  const layer = css.slice(css.indexOf('@layer components'));
  const rule = (selector: string) => {
    const at = layer.indexOf(`${selector} {`);
    expect(at, `${selector} is defined`).toBeGreaterThan(-1);
    return layer.slice(at, layer.indexOf('}', at));
  };

  it.each([
    ['.count-pill', 'var(--chip)'],
    ['.stat-tile', 'var(--card)'],
    ['.stat-tile-value', 'font-size: 24px'],
    ['.data-table tbody th', 'border-top: 1px solid var(--line)'],
    ['.board-column', 'border-radius: 22px'],
    ['.kv-grid', 'grid-template-columns'],
    ['.field-label', 'var(--text-secondary)'],
    ['.choice-card', 'border-radius: 14px'],
    ['.field-group', 'var(--hairline)'],
  ])('%s carries its recipe', (selector, fragment) => {
    expect(rule(selector)).toContain(fragment);
  });

  it('.tag colours sit at zero specificity so a .pill-* tone wins', () => {
    expect(rule(':where(.tag)')).toContain('background: var(--subtle)');
    // .tag shares its geometry rule with .pill-sm; that rule sets no colour.
    expect(rule('.pill-sm')).not.toContain('background');
  });

  it('a checked choice card takes the accent tint', () => {
    expect(layer).toMatch(
      /\.choice-card:has\(input:checked\)\s*\{\s*background: linear-gradient\(var\(--accent-soft\)/,
    );
  });
});
