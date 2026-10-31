import { useMemo } from 'react';
import { useMatch } from 'react-router';
import { useShallow } from 'zustand/react/shallow';
import { useItervoxStore } from '../store/itervoxStore';
import { pickFocusIssue, type FocusPick } from '../utils/focusPick';

/** The issue view's route: `/focus` picks an issue, `/focus/:identifier` pins one. */
export const FOCUS_PATH = '/focus';

export const focusPathFor = (identifier: string) =>
  `${FOCUS_PATH}/${encodeURIComponent(identifier)}`;

/** The automatic pick (`/focus`), from the snapshot. */
export function useFocusPick(): FocusPick | null {
  const { running, history, inputRequired } = useItervoxStore(
    useShallow((s) => ({
      running: s.snapshot?.running,
      history: s.snapshot?.history,
      inputRequired: s.snapshot?.inputRequired,
    })),
  );
  return useMemo(
    () => (running ? pickFocusIssue({ running, history, inputRequired }) : null),
    [running, history, inputRequired],
  );
}

/**
 * The issue the issue view shows, read anywhere in the router (the top bar
 * names it): the URL's identifier, else the automatic pick. null off the
 * issue view, or when there is nothing to show.
 */
export function useFocusIssue(): { identifier: string; pinned: boolean } | null {
  const pinned = useMatch(`${FOCUS_PATH}/:identifier`);
  const auto = useMatch(FOCUS_PATH);
  const pick = useFocusPick();
  const id = pinned?.params.identifier;
  if (id) return { identifier: id, pinned: true };
  if (auto && pick) return { identifier: pick.identifier, pinned: false };
  return null;
}
