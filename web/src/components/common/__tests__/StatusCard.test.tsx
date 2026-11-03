import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { StatusCard, StatusScreen } from '../StatusCard';

describe('StatusCard', () => {
  it('renders eyebrow, an h1 title and body on a glass panel', () => {
    const { container } = render(
      <StatusScreen>
        <StatusCard eyebrow="Daemon unreachable" title="Can't reach the daemon">
          <p>Retrying.</p>
        </StatusCard>
      </StatusScreen>,
    );
    const heading = screen.getByRole('heading', { level: 1, name: "Can't reach the daemon" });
    const card = heading.closest('section');
    expect(card).toHaveClass('glass-panel');
    expect(screen.getByText('Daemon unreachable')).toHaveClass('eyebrow');
    expect(screen.getByText('Retrying.')).toBeInTheDocument();
    expect(container.querySelector('.brand-tile')).toBeNull();
  });

  it('takes a heading level, the brand tile and a div root', () => {
    const { container } = render(<StatusCard as="div" level={2} brand title="Sign in" />);
    expect(screen.getByRole('heading', { level: 2, name: 'Sign in' })).toBeInTheDocument();
    expect(container.querySelector('.brand-tile')).not.toBeNull();
    expect(container.firstElementChild?.tagName).toBe('DIV');
  });
});
