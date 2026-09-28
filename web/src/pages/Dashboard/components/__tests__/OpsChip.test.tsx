// CORE-086 — OpsChip renders a router link only when given a destination.
import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { OpsChip } from '../OpsChip';

describe('OpsChip', () => {
  it('renders a link when to is provided', () => {
    render(
      <MemoryRouter>
        <OpsChip label="Retrying 2" warning to="/#retry-queue" title="2 retrying" />
      </MemoryRouter>,
    );
    const link = screen.getByRole('link', { name: /Retrying 2/ });
    expect(link).toHaveAttribute('href', '/#retry-queue');
    expect(link.className).toContain('text-theme-warning-text');
  });

  it('renders a span when to is omitted', () => {
    // No router: a static chip must not need one.
    render(<OpsChip label="Capacity 1/3" />);
    expect(screen.queryByRole('link')).not.toBeInTheDocument();
    const chip = screen.getByText('Capacity 1/3');
    expect(chip.tagName).toBe('SPAN');
  });
});
