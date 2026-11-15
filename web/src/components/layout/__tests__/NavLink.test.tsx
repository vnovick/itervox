import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { describe, it, expect } from 'vitest';
import { NavLink } from '../NavLink';

function renderWithRouter(ui: React.ReactElement, { initialEntries = ['/'] } = {}) {
  return render(<MemoryRouter initialEntries={initialEntries}>{ui}</MemoryRouter>);
}

describe('NavLink', () => {
  it('renders a link element', () => {
    renderWithRouter(<NavLink to="/dashboard" icon="◫" label="Dashboard" />);
    expect(screen.getByRole('link')).toBeInTheDocument();
  });

  it('has an accessible aria-label from the label prop', () => {
    renderWithRouter(<NavLink to="/dashboard" icon="◫" label="Dashboard" />);
    expect(screen.getByRole('link')).toHaveAttribute('aria-label', 'Dashboard');
  });

  it('renders the icon character', () => {
    renderWithRouter(<NavLink to="/logs" icon="⌨" label="Logs" />);
    expect(screen.getByText('⌨')).toBeInTheDocument();
  });

  it('links to the correct path', () => {
    renderWithRouter(<NavLink to="/settings" icon="⚙" label="Settings" />);
    expect(screen.getByRole('link')).toHaveAttribute('href', '/settings');
  });

  it('applies active class when route matches', () => {
    renderWithRouter(<NavLink to="/dashboard" icon="◫" label="Dashboard" />, {
      initialEntries: ['/dashboard'],
    });
    expect(screen.getByRole('link')).toHaveAttribute('data-active', 'true');
  });

  it('does not apply active class when route does not match', () => {
    renderWithRouter(<NavLink to="/settings" icon="⚙" label="Settings" />, {
      initialEntries: ['/dashboard'],
    });
    const link = screen.getByRole('link');
    expect(link).not.toHaveAttribute('data-active', 'true');
  });

  it('active link has aria-current=page', () => {
    renderWithRouter(<NavLink to="/dashboard" icon="◫" label="Dashboard" />, {
      initialEntries: ['/dashboard'],
    });
    expect(screen.getByRole('link')).toHaveAttribute('aria-current', 'page');
  });

  it('inactive link has no aria-current', () => {
    renderWithRouter(<NavLink to="/settings" icon="⚙" label="Settings" />, {
      initialEntries: ['/dashboard'],
    });
    expect(screen.getByRole('link')).not.toHaveAttribute('aria-current');
  });

  // Visual refresh — labelled sidebar rows.
  it('shows the label as visible text next to the icon', () => {
    renderWithRouter(<NavLink to="/logs" icon="⌨" label="Logs" />);
    const link = screen.getByRole('link', { name: 'Logs' });
    expect(link).toHaveClass('nav-item');
    expect(link).toHaveTextContent('Logs');
  });

  it('renders the count as a small pill and folds it into the name', () => {
    renderWithRouter(
      <NavLink to="/memory" icon="◷" label="Memory" badge={2} badgeLabel="lessons to review" />,
    );
    const link = screen.getByRole('link', { name: 'Memory, 2 lessons to review' });
    const badge = screen.getByTestId('nav-badge');
    expect(badge).toHaveClass('nav-count');
    expect(badge).toHaveTextContent('2');
    expect(link).toContainElement(badge);
  });
});
