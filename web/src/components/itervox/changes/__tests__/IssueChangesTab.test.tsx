import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import IssueChangesTab from '../IssueChangesTab';
import { parseUnifiedDiff } from '../diffParse';
import { routeFetch } from './fetchMock';

vi.mock('../../../../auth/authedFetch', () => ({ authedFetch: vi.fn() }));
import { authedFetch } from '../../../../auth/authedFetch';
const mockFetch = vi.mocked(authedFetch);

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

const diff = (hunksAllowed: boolean) => ({
  identifier: 'ENG-1',
  base: 'main',
  mergeBase: '0123456789abcdef0123',
  hunksAllowed,
  files: [
    { path: 'a.go', status: 'modified', additions: 2, deletions: 1 },
    { path: 'logo.png', status: 'added', additions: 0, deletions: 0, binary: true },
  ],
});

const patch =
  'diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,3 @@\n package a\n-old()\n+<img src=x onerror=alert(1)>\n+new()\n';

describe('IssueChangesTab (#215)', () => {
  beforeEach(() => {
    mockFetch.mockReset();
  });

  it('lists the files and shows a selected file as a unified diff rendered as text', async () => {
    routeFetch(mockFetch, {
      '/api/v1/issues/ENG-1/diff': { body: diff(true) },
      '/api/v1/issues/ENG-1/diff?file=a.go': {
        body: {
          identifier: 'ENG-1',
          base: 'main',
          file: diff(true).files[0],
          patch,
          bytes: patch.length,
          maxBytes: 262144,
        },
      },
    });
    const { container } = render(<IssueChangesTab identifier="ENG-1" />, { wrapper });
    expect(await screen.findByText(/merge base/)).toHaveTextContent('0123456789ab');
    expect(screen.getByText('logo.png')).toBeInTheDocument();
    expect(screen.getByText('binary')).toBeInTheDocument();

    await userEvent.click(screen.getByRole('button', { name: /a\.go/ }));
    const viewer = await screen.findByTestId('diff-viewer');
    expect(viewer).toHaveTextContent('+<img src=x onerror=alert(1)>');
    expect(container.querySelector('img')).toBeNull();
    expect(screen.getByText('-old()').className).toContain('danger');
    expect(screen.getByText('+new()').className).toContain('success');
    expect(screen.getByText('@@ -1,2 +1,3 @@').className).toContain('accent');
  });

  it('shows a truncation notice and a binary placeholder', async () => {
    routeFetch(mockFetch, {
      '/api/v1/issues/ENG-1/diff': { body: diff(true) },
      '/api/v1/issues/ENG-1/diff?file=a.go': {
        body: {
          identifier: 'ENG-1',
          base: 'main',
          file: diff(true).files[0],
          patch,
          truncated: true,
          bytes: 600 * 1024,
          maxBytes: 256 * 1024,
        },
      },
      '/api/v1/issues/ENG-1/diff?file=logo.png': {
        body: {
          identifier: 'ENG-1',
          base: 'main',
          file: diff(true).files[1],
          patch: '',
          bytes: 0,
          maxBytes: 262144,
        },
      },
    });
    render(<IssueChangesTab identifier="ENG-1" />, { wrapper });
    await userEvent.click(await screen.findByRole('button', { name: /a\.go/ }));
    expect(await screen.findByText('Diff truncated at 256 KB of 600 KB.')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: /logo\.png/ }));
    expect(await screen.findByText(/Binary file/)).toBeInTheDocument();
  });

  it('keeps a viewer to the file list: no buttons, no hunk request', async () => {
    routeFetch(mockFetch, { '/api/v1/issues/ENG-1/diff': { body: diff(false) } });
    render(<IssueChangesTab identifier="ENG-1" />, { wrapper });
    expect(await screen.findByText('a.go')).toBeInTheDocument();
    expect(screen.queryByRole('button')).toBeNull();
    expect(screen.getByText(/File diffs need an operator token/)).toBeInTheDocument();
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it('says so when the issue has no worktree', async () => {
    routeFetch(mockFetch, {
      '/api/v1/issues/ENG-1/diff': {
        status: 404,
        body: { error: { code: 'no_worktree', message: 'the issue has no worktree' } },
      },
    });
    render(<IssueChangesTab identifier="ENG-1" />, { wrapper });
    expect(await screen.findByText(/No worktree for this issue/)).toBeInTheDocument();
  });

  it('parses unified diff lines by kind', () => {
    expect(parseUnifiedDiff(patch).map((l) => l.kind)).toEqual([
      'meta',
      'meta',
      'meta',
      'hunk',
      'ctx',
      'del',
      'add',
      'add',
    ]);
  });
});
