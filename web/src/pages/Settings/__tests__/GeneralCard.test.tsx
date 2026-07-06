import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { GeneralCard } from '../GeneralCard';

describe('GeneralCard', () => {
  it('saves inline-input changes', async () => {
    const onSetInlineInput = vi.fn().mockResolvedValue(true);

    render(<GeneralCard inlineInput={false} onSetInlineInput={onSetInlineInput} />);

    fireEvent.click(screen.getByLabelText(/inline input via tracker comments/i));

    await waitFor(() => {
      expect(onSetInlineInput).toHaveBeenCalledWith(true);
    });
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('shows an error when saving fails', async () => {
    const onSetInlineInput = vi.fn().mockResolvedValue(false);

    render(<GeneralCard inlineInput={true} onSetInlineInput={onSetInlineInput} />);

    fireEvent.click(screen.getByLabelText(/inline input via tracker comments/i));

    expect(await screen.findByRole('alert')).toHaveTextContent(/failed to save inline-input/i);
  });

  it('uses the visible Resuming panel name in inline-input help text', () => {
    render(<GeneralCard inlineInput={false} onSetInlineInput={vi.fn()} />);

    expect(screen.getByText(/dashboard.s .Resuming. panel/i)).toBeInTheDocument();
    expect(screen.queryByText(/Pending Resume/i)).not.toBeInTheDocument();
  });
});
