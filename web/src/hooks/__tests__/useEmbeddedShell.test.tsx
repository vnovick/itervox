import { afterEach, describe, expect, it } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import { MemoryRouter, useLocation } from 'react-router';
import {
  detectEmbedded,
  isEmbedded,
  isEmbeddedLanding,
  isInAppPath,
  resetEmbeddedForTests,
  useEmbeddedNavigateHook,
} from '../useEmbeddedShell';

const win = (shell: string | undefined, search = '') =>
  ({ __ITERVOX_SHELL__: shell, location: { search } }) as unknown as Window;

afterEach(() => {
  resetEmbeddedForTests();
  delete window.__ITERVOX_SHELL__;
  delete window.__itervoxNavigate;
});

describe('embedded shell flag', () => {
  it('is set by the desktop app global, in any build', () => {
    expect(detectEmbedded(win('desktop'), false)).toBe(true);
    expect(detectEmbedded(win(undefined), false)).toBe(false);
    expect(detectEmbedded(win('browser'), true)).toBe(false);
  });

  it('honours ?embedded=1 only in the dev server (the e2e harness)', () => {
    expect(detectEmbedded(win(undefined, '?embedded=1'), true)).toBe(true);
    expect(detectEmbedded(win(undefined, '?embedded=1'), false)).toBe(false);
    expect(detectEmbedded(win(undefined, '?embedded=0'), true)).toBe(false);
  });

  it('is read once per page load', () => {
    window.__ITERVOX_SHELL__ = 'desktop';
    expect(isEmbedded()).toBe(true);
    delete window.__ITERVOX_SHELL__;
    expect(isEmbedded()).toBe(true);
    resetEmbeddedForTests();
    expect(isEmbedded()).toBe(false);
  });
});

describe('isInAppPath', () => {
  it.each(['/', '/focus', '/focus/ENG-1', '/?newTask=1', '/logs/owner%2Frepo%2312'])(
    'accepts %s',
    (p) => {
      expect(isInAppPath(p)).toBe(true);
    },
  );
  it.each(['', 'focus', '//evil.example/x', 'https://evil.example', '/\\evil', '/a\nb', 42])(
    'refuses %s',
    (p) => {
      expect(isInAppPath(p)).toBe(false);
    },
  );
});

describe('isEmbeddedLanding', () => {
  it('lands on the issue view on the first entry of a bare "/" only', () => {
    resetEmbeddedForTests(true);
    expect(isEmbeddedLanding({ search: '', key: 'default' })).toBe(true);
    expect(isEmbeddedLanding({ search: '?embedded=1', key: 'default' })).toBe(true);
    expect(isEmbeddedLanding({ search: '?token=x', key: 'default' })).toBe(true);
    // A deep link keeps the board; so does a later visit (the Board button).
    expect(isEmbeddedLanding({ search: '?issue=ENG-1', key: 'default' })).toBe(false);
    expect(isEmbeddedLanding({ search: '?newTask=1', key: 'default' })).toBe(false);
    expect(isEmbeddedLanding({ search: '', key: 'k2' })).toBe(false);
  });

  it('never lands when not embedded', () => {
    resetEmbeddedForTests(false);
    expect(isEmbeddedLanding({ search: '', key: 'default' })).toBe(false);
  });
});

function Probe() {
  useEmbeddedNavigateHook();
  const loc = useLocation();
  return <p data-testid="where">{loc.pathname + loc.search}</p>;
}

describe('useEmbeddedNavigateHook (the Go menu)', () => {
  it('embedded: exposes window.__itervoxNavigate, which navigates in-app paths only', () => {
    resetEmbeddedForTests(true);
    const { unmount } = render(
      <MemoryRouter>
        <Probe />
      </MemoryRouter>,
    );
    const nav = window.__itervoxNavigate;
    expect(nav).toBeTypeOf('function');
    let ok = false;
    act(() => {
      ok = nav?.('/focus/ENG-1') ?? false;
    });
    expect(ok).toBe(true);
    expect(screen.getByTestId('where')).toHaveTextContent('/focus/ENG-1');
    act(() => {
      ok = nav?.('https://evil.example/') ?? true;
    });
    expect(ok).toBe(false);
    expect(screen.getByTestId('where')).toHaveTextContent('/focus/ENG-1');
    unmount();
    expect(window.__itervoxNavigate).toBeUndefined();
  });

  it('not embedded: no hook', () => {
    resetEmbeddedForTests(false);
    render(
      <MemoryRouter>
        <Probe />
      </MemoryRouter>,
    );
    expect(window.__itervoxNavigate).toBeUndefined();
  });
});
