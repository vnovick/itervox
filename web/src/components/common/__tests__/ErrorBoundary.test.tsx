import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';

const reportClientError = vi.fn();
vi.mock('../../../lib/clientErrorReporter', () => ({
  reportClientError: (...args: unknown[]) => {
    reportClientError(...args);
  },
}));

import { ErrorBoundary } from '../ErrorBoundary';
import { PageErrorBoundary } from '../PageErrorBoundary';

function Thrower(): never {
  throw new Error('render exploded');
}

beforeEach(() => {
  vi.stubEnv('DEV', false);
  reportClientError.mockReset();
  vi.spyOn(console, 'error').mockImplementation(() => undefined);
});

describe('ErrorBoundary', () => {
  it('reports caught error via reportClientError', () => {
    render(
      <ErrorBoundary>
        <Thrower />
      </ErrorBoundary>,
    );
    expect(screen.getByText('render exploded')).toBeTruthy();
    expect(reportClientError).toHaveBeenCalledTimes(1);
    expect(reportClientError.mock.calls[0][0]).toMatchObject({
      kind: 'render',
      message: 'render exploded',
    });
  });
});

describe('PageErrorBoundary', () => {
  it('reports caught error via reportClientError', () => {
    render(
      <PageErrorBoundary>
        <Thrower />
      </PageErrorBoundary>,
    );
    expect(screen.getByText('render exploded')).toBeTruthy();
    expect(reportClientError).toHaveBeenCalledTimes(1);
    expect(reportClientError.mock.calls[0][0]).toMatchObject({
      kind: 'render',
      message: 'render exploded',
    });
  });
});
