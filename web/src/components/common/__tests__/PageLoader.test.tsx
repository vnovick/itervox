// CORE-089 — the route-level Suspense fallback is announced, not a silent spinner.
import { describe, expect, it } from 'vitest';
import { render, screen } from '@testing-library/react';
import { PageLoader } from '../PageLoader';

describe('PageLoader', () => {
  it('has role=status with loading text', () => {
    render(<PageLoader />);
    expect(screen.getByRole('status')).toHaveTextContent(/loading page/i);
  });
});
