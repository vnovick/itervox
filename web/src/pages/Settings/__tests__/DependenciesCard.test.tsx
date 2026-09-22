import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { DependenciesCard } from '../DependenciesCard';

describe('DependenciesCard', () => {
  let onSetMode: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    onSetMode = vi.fn().mockResolvedValue(true);
  });

  it('renders both options with the current mode selected', () => {
    render(<DependenciesCard mode="auto" onSetMode={onSetMode} />);

    expect(screen.getByRole('radio', { name: 'Auto' })).toBeChecked();
    expect(screen.getByRole('radio', { name: 'Manual' })).not.toBeChecked();
  });

  it('reflects a manual mode as selected', () => {
    render(<DependenciesCard mode="manual" onSetMode={onSetMode} />);

    expect(screen.getByRole('radio', { name: 'Manual' })).toBeChecked();
    expect(screen.getByRole('radio', { name: 'Auto' })).not.toBeChecked();
  });

  it('calls onSetMode when choosing "manual"', async () => {
    render(<DependenciesCard mode="auto" onSetMode={onSetMode} />);

    fireEvent.click(screen.getByRole('radio', { name: 'Manual' }));

    await waitFor(() => {
      expect(onSetMode).toHaveBeenCalledWith('manual');
    });
  });

  it('shows an error line when onSetMode resolves false', async () => {
    onSetMode.mockResolvedValue(false);
    render(<DependenciesCard mode="auto" onSetMode={onSetMode} />);

    fireEvent.click(screen.getByRole('radio', { name: 'Manual' }));

    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent(/dependency analysis mode/i);
    });
  });

  it('exposes the control via a stable testid', () => {
    render(<DependenciesCard mode="auto" onSetMode={onSetMode} />);

    expect(screen.getByTestId('deps-analysis-mode')).toBeInTheDocument();
  });
});
