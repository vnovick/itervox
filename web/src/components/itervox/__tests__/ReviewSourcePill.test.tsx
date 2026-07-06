import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { ReviewSourcePill } from '../ReviewSourcePill';

describe('ReviewSourcePill', () => {
  it('renders the green "completed this session" pill for source = "session"', () => {
    render(<ReviewSourcePill source="session" />);
    const el = screen.getByText(/completed this session/i);
    expect(el).toBeInTheDocument();
    // Tone marker — green tones are encoded with emerald classes per the
    // notifications_plan §C palette.
    expect(el.className).toMatch(/emerald/);
  });

  it('renders the muted "in review (tracker)" pill for source = "tracker"', () => {
    render(<ReviewSourcePill source="tracker" />);
    expect(screen.getByText(/in review \(tracker\)/i)).toBeInTheDocument();
  });
});
