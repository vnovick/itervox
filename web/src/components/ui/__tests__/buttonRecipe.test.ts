import { describe, expect, it } from 'vitest';
import { plainButtonClass } from '../button/buttonRecipe';
import { buttonClass } from '../button/buttonStyles';

const sorted = (s: string) => s.split(/\s+/).filter(Boolean).sort();

describe('plainButtonClass', () => {
  it.each([
    ['primary', 'lg'],
    ['secondary', 'sm'],
    ['danger', 'xs'],
  ] as const)('matches buttonClass for %s/%s', (variant, size) => {
    expect(sorted(plainButtonClass(variant, size))).toEqual(sorted(buttonClass(variant, size)));
  });

  it('appends layout extras and the pill shape', () => {
    const cls = plainButtonClass('primary', 'md', { extra: 'w-full', pill: true });
    expect(cls).toContain('w-full');
    expect(cls).toContain('rounded-full');
    expect(cls).not.toContain('rounded-[var(--radius-sm)]');
  });
});
