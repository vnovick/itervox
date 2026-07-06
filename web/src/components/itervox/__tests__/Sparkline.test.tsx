import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Sparkline } from '../Sparkline';

describe('Sparkline', () => {
  it('renders a non-empty path for a non-zero series', () => {
    const { container } = render(
      <Sparkline values={[0, 1, 2, 1, 0]} height={32} color="#00ff00" ariaLabel="series" />,
    );
    const path = container.querySelector('path');
    expect(path).not.toBeNull();
    const d = path?.getAttribute('d') ?? '';
    expect(d).not.toBe('');
    // Path must include both 'M' (move) and 'L' (line) instructions to be
    // visible — a flat-line fallback only emits two coords.
    expect(d.split('L').length).toBeGreaterThan(1);
  });

  it('renders a flat baseline for an empty series', () => {
    const { container } = render(
      <Sparkline values={[]} height={20} color="red" ariaLabel="empty series" />,
    );
    const d = container.querySelector('path')?.getAttribute('d') ?? '';
    // Two coords on the bottom edge — width defaults to 100, height to 20.
    expect(d).toBe('M0 20 L100 20');
  });

  it('renders a flat baseline for an all-zero series', () => {
    const { container } = render(
      <Sparkline values={[0, 0, 0]} height={20} color="red" ariaLabel="zero series" />,
    );
    const d = container.querySelector('path')?.getAttribute('d') ?? '';
    expect(d).toBe('M0 20 L100 20');
  });

  it('exposes the aria-label and a <title> for screen readers / hover tooltip', () => {
    render(<Sparkline values={[1, 2]} height={20} color="red" ariaLabel="my chart" />);
    const svg = screen.getByRole('img', { name: 'my chart' });
    expect(svg).toBeInTheDocument();
    expect(svg.querySelector('title')?.textContent).toBe('my chart');
  });

  it('keeps coordinates within the viewBox bounds', () => {
    const { container } = render(
      <Sparkline values={[10, 5, 20, 0]} height={40} color="blue" ariaLabel="bounded" />,
    );
    const d = container.querySelector('path')?.getAttribute('d') ?? '';
    const numbers = d.replace(/[ML]/g, ' ').split(/\s+/).filter(Boolean).map(Number);
    for (let i = 0; i < numbers.length; i += 2) {
      const x = numbers[i];
      const y = numbers[i + 1];
      expect(x).toBeGreaterThanOrEqual(0);
      expect(x).toBeLessThanOrEqual(100);
      expect(y).toBeGreaterThanOrEqual(0);
      expect(y).toBeLessThanOrEqual(40);
    }
  });

  it('records the series max in a data attribute for snapshot diffing', () => {
    render(<Sparkline values={[3, 7, 2]} height={20} color="red" ariaLabel="max" />);
    const svg = screen.getByRole('img', { name: 'max' });
    expect(svg.getAttribute('data-max')).toBe('7');
  });
});
