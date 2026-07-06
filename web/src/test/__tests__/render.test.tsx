import { afterEach, describe, expect, it } from 'vitest';
import { cleanup, screen } from '@testing-library/react';
import { useLocation } from 'react-router';
import { useItervoxStore } from '../../store/itervoxStore';
import { useUIStore } from '../../store/uiStore';
import { render } from '../render';
import { resetAllStores } from '../resetStores';

afterEach(() => {
  cleanup();
  resetAllStores();
});

function ShowLocation() {
  const location = useLocation();
  return <div data-testid="route">{location.pathname}</div>;
}

describe('render harness', () => {
  it('resets stores before each render so leftover state does not leak', () => {
    useUIStore.setState({ dashboardSearch: 'leftover' });
    expect(useUIStore.getState().dashboardSearch).toBe('leftover');

    render(<div data-testid="ok">ok</div>);

    expect(useUIStore.getState().dashboardSearch).toBe('');
    expect(screen.getByTestId('ok')).toBeInTheDocument();
  });

  it('lands on the requested route', () => {
    render(<ShowLocation />, { route: '/timeline' });
    expect(screen.getByTestId('route').textContent).toBe('/timeline');
  });

  it('auth: token-entry hides children and renders TokenEntryScreen', () => {
    render(<div data-testid="children">children</div>, { auth: 'token-entry' });
    expect(screen.queryByTestId('children')).not.toBeInTheDocument();
    expect(screen.getByText(/Sign in to Itervox/)).toBeInTheDocument();
  });

  it('auth: server-down hides children and renders ServerDownScreen', () => {
    render(<div data-testid="children">children</div>, { auth: 'server-down' });
    expect(screen.queryByTestId('children')).not.toBeInTheDocument();
    expect(screen.getByText(/Can't reach the daemon/)).toBeInTheDocument();
  });

  it('auth: authorized shows the children directly', () => {
    render(<div data-testid="children">children</div>, { auth: 'authorized' });
    expect(screen.getByTestId('children')).toBeInTheDocument();
  });

  it('seeds the snapshot store with the provided snapshot by default', () => {
    render(<div />);
    const snapshot = useItervoxStore.getState().snapshot;
    expect(snapshot).not.toBeNull();
    expect(snapshot?.projectName).toBe('Quickstart Demo');
  });

  it('snapshot: null leaves the store empty', () => {
    render(<div />, { snapshot: null });
    expect(useItervoxStore.getState().snapshot).toBeNull();
  });

  it('resetAllStores is idempotent (calling twice is a no-op)', () => {
    resetAllStores();
    resetAllStores();
    expect(useItervoxStore.getState().snapshot).toBeNull();
    expect(useUIStore.getState().dashboardSearch).toBe('');
  });
});
