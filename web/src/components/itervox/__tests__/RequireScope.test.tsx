import { render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';
import { useItervoxStore } from '../../../store/itervoxStore';
import { makeSnapshot } from '../../../test/fixtures/snapshots';
import { normalizeScope, scopeAllows } from '../../../auth/scope';
import { RequireScope } from '../RequireScope';

// #214 — scope-aware hiding.

function ui() {
  return (
    <>
      <RequireScope scope="viewer">
        <span>view</span>
      </RequireScope>
      <RequireScope scope="operator" fallback={<span>needs operator</span>}>
        <button type="button">pause</button>
      </RequireScope>
      <RequireScope scope="admin">
        <button type="button">settings</button>
      </RequireScope>
    </>
  );
}

beforeEach(() => {
  useItervoxStore.setState({ snapshot: null });
});

describe('RequireScope', () => {
  it('shows a viewer only viewer content and the fallback', () => {
    useItervoxStore.setState({ snapshot: makeSnapshot({ callerScope: 'viewer' }) });
    render(ui());
    expect(screen.getByText('view')).toBeInTheDocument();
    expect(screen.getByText('needs operator')).toBeInTheDocument();
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });

  it('shows an operator the operator controls but not admin ones', () => {
    useItervoxStore.setState({ snapshot: makeSnapshot({ callerScope: 'operator' }) });
    render(ui());
    expect(screen.getByRole('button', { name: 'pause' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'settings' })).not.toBeInTheDocument();
  });

  it('treats a daemon without callerScope as admin, and no snapshot yet as viewer', () => {
    useItervoxStore.setState({ snapshot: makeSnapshot({}) });
    const { unmount } = render(ui());
    expect(screen.getByRole('button', { name: 'settings' })).toBeInTheDocument();
    unmount();
    useItervoxStore.setState({ snapshot: null });
    render(ui());
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });

  it('orders the scopes and fails closed on unknown values', () => {
    expect(scopeAllows('admin', 'operator')).toBe(true);
    expect(scopeAllows('operator', 'admin')).toBe(false);
    expect(normalizeScope('root')).toBe('viewer');
    expect(normalizeScope(undefined)).toBe('admin');
  });
});
