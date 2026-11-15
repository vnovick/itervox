// Returns the total count of items rendered in the Notifications tab.
// #211 — reads the snapshot through the same narrow `useShallow` selector as
// the attention inbox (`useOperatorQueueInput`), never the whole snapshot:
// `generatedAt` moves on every SSE frame, and a whole-snapshot subscription
// re-rendered DashboardIssuesPanel (and the board under it) on each one.

import type { TrackerIssue } from '../../../types/schemas';
import { useOperatorQueueFor } from '../../../hooks/useOperatorQueue';

export function useNotificationsTotal(issues: readonly TrackerIssue[]): number {
  // CORE-078 — the attention count (read-only resuming rows excluded), so
  // the tab label matches the inbox, the nav badge and the page title.
  return useOperatorQueueFor(issues).attention;
}
