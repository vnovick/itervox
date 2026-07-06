import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { WorkspaceCard } from '../WorkspaceCard';

describe('WorkspaceCard', () => {
  let onToggle: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    onToggle = vi.fn().mockResolvedValue(true);
  });

  it('calls onToggle when enabling auto-clear is allowed', async () => {
    render(
      <WorkspaceCard autoClearWorkspace={false} autoReviewEnabled={false} onToggle={onToggle} />,
    );

    fireEvent.click(screen.getByRole('checkbox'));

    await waitFor(() => {
      expect(onToggle).toHaveBeenCalledWith(true);
    });
  });

  it('blocks enabling auto-clear while auto-review is enabled', () => {
    render(
      <WorkspaceCard autoClearWorkspace={false} autoReviewEnabled={true} onToggle={onToggle} />,
    );

    fireEvent.click(screen.getByRole('checkbox'));

    expect(onToggle).not.toHaveBeenCalled();
    expect(screen.getByRole('alert')).toHaveTextContent(/auto-review/i);
  });

  it('hides the auto-review conflict once auto-review is disabled', () => {
    const { rerender } = render(
      <WorkspaceCard autoClearWorkspace={false} autoReviewEnabled={true} onToggle={onToggle} />,
    );

    fireEvent.click(screen.getByRole('checkbox'));
    expect(screen.getByRole('alert')).toHaveTextContent(/auto-review/i);

    rerender(
      <WorkspaceCard autoClearWorkspace={false} autoReviewEnabled={false} onToggle={onToggle} />,
    );

    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });
});
