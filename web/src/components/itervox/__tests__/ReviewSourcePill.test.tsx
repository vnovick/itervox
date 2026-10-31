import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { ReviewSourcePill } from '../ReviewSourcePill';

describe('ReviewSourcePill', () => {
  it('renders the green "completed this session" pill for source = "session"', () => {
    render(<ReviewSourcePill source="session" />);
    const el = screen.getByText(/completed this session/i);
    expect(el).toBeInTheDocument();
    // Tone marker — the green tone is the success token (visual refresh:
    // the emerald palette classes became theme tokens).
    expect(el.className).toMatch(/theme-success/);
  });

  it('renders the muted "in review (tracker)" pill for source = "tracker"', () => {
    render(<ReviewSourcePill source="tracker" />);
    expect(screen.getByText(/in review \(tracker\)/i)).toBeInTheDocument();
  });
});
