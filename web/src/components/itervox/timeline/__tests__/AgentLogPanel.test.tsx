import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { render } from '@testing-library/react';
import { AgentLogPanel } from '../AgentLogPanel';
import { useItervoxStore } from '../../../../store/itervoxStore';
import { makeSnapshot } from '../../../../test/fixtures/snapshots';
import type { IssueLogEntry } from '../../../../types/schemas';

vi.mock('../../../../queries/logs', () => ({
  useIssueLogs: () => ({ data: [] as IssueLogEntry[] }),
}));

// CORE-024 fix round 1 — scrollIntoView's `behavior` is an explicit
// argument, so the CSS `scroll-behavior: auto !important` under
// prefers-reduced-motion (index.css) cannot override it; the component must
// pass 'auto' itself. Independently testable via a spied scrollIntoView —
// no computed styles involved. AgentLogPanel.tsx is excluded from the
// coverage gate (extracted timeline presentational component, per
// vitest.config.ts), but the behavior still needs a real, passing test.
describe('AgentLogPanel — reduced-motion scroll (CORE-024)', () => {
  const scrollIntoViewSpy = vi.fn();

  beforeEach(() => {
    scrollIntoViewSpy.mockClear();
    Element.prototype.scrollIntoView = scrollIntoViewSpy;
    useItervoxStore.setState({ snapshot: makeSnapshot() });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('scrolls smoothly to the bottom by default', () => {
    render(
      <AgentLogPanel
        identifier="ENG-1"
        logSlice={[{ event: 'text', message: 'hello', level: 'INFO', tool: '', time: '' }]}
      />,
    );
    expect(scrollIntoViewSpy).toHaveBeenCalledWith({ behavior: 'smooth' });
  });

  it('scrolls without animation when the user prefers reduced motion', () => {
    vi.stubGlobal(
      'matchMedia',
      vi.fn().mockReturnValue({
        matches: true,
        media: '(prefers-reduced-motion: reduce)',
        addEventListener: () => undefined,
        removeEventListener: () => undefined,
      }),
    );
    render(
      <AgentLogPanel
        identifier="ENG-1"
        logSlice={[{ event: 'text', message: 'hello', level: 'INFO', tool: '', time: '' }]}
      />,
    );
    expect(scrollIntoViewSpy).toHaveBeenCalledWith({ behavior: 'auto' });
  });
});
