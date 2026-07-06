import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';

import { DetailRow } from '../AutomationQueueDetailBits';

describe('DetailRow (v0.2.0 audit P1-5 + P1-6)', () => {
  it('renders the dash placeholder for undefined', () => {
    render(<DetailRow label="Limit" value={undefined} />);
    expect(screen.getByText('-')).toBeInTheDocument();
  });

  it('renders the dash placeholder for null', () => {
    render(<DetailRow label="Limit" value={null} />);
    expect(screen.getByText('-')).toBeInTheDocument();
  });

  it('renders the dash placeholder for the empty string', () => {
    render(<DetailRow label="Limit" value={''} />);
    expect(screen.getByText('-')).toBeInTheDocument();
  });

  it('renders the dash placeholder for year-0001 sentinel time strings', () => {
    render(<DetailRow label="Last audit" value="0001-01-01T00:00:00Z" />);
    expect(screen.getByText('-')).toBeInTheDocument();
  });

  it('renders 0 as a meaningful value, NOT the dash placeholder', () => {
    render(<DetailRow label="Limit" value={0} />);
    // The dash placeholder must not appear; the literal "0" must.
    expect(screen.queryByText('-')).toBeNull();
    expect(screen.getByText('0')).toBeInTheDocument();
  });

  it('renders false as a meaningful value, NOT the dash placeholder', () => {
    render(<DetailRow label="Enabled" value={false as unknown as React.ReactNode} />);
    // `false` is a valid ReactNode but renders to nothing in the DOM, so we
    // just assert the dash placeholder is not used (the old code would have
    // shown it because `false || <dash>` evaluates the dash branch).
    expect(screen.queryByText('-')).toBeNull();
  });

  it('renders a real value untouched', () => {
    render(<DetailRow label="Profile" value="implementer" />);
    expect(screen.getByText('implementer')).toBeInTheDocument();
  });
});
