import { useCallback } from 'react';
import { useLocation, type To } from 'react-router';
import { mergeSearch } from './useUrlState';

// CORE-086 — where a count in the header or the live-ops strip leads. The
// dashboard has no orchestrator-state filter (CORE-079 filters by tracker
// state pill, search and view), so a count links to the dashboard section
// that lists those items, or to the dashboard view that shows them.
//
//   running, paused     -> RunningSessionsTable   (/#running-sessions)
//   needs input         -> AttentionInbox         (/#attention-inbox)
//   resuming            -> PendingResumePanel     (/#pending-resume)
//   retrying            -> RetryQueueTable        (/#retry-queue)
//   outbox              -> OutboxList             (/#outbox)
//   deps blocked        -> the Deps view          (/?view=deps)
export type DashboardTarget =
  | 'running-sessions'
  | 'attention-inbox'
  | 'pending-resume'
  | 'retry-queue'
  | 'outbox'
  | 'deps-view';

/**
 * Returns a builder for dashboard link targets. On the dashboard itself the
 * current query (view, search, state filter, open issue) is kept, so following
 * a count never resets the operator's view; from any other page the link
 * starts from a bare `/`, which the dashboard treats as "keep the stored
 * view" (BH-M5-1).
 */
export function useDashboardHref(): (target: DashboardTarget) => To {
  const { pathname, search } = useLocation();
  const base = pathname === '/' ? search : '';
  return useCallback(
    (target: DashboardTarget): To =>
      target === 'deps-view'
        ? { pathname: '/', search: mergeSearch(base, { view: 'deps' }) }
        : { pathname: '/', search: mergeSearch(base, {}), hash: `#${target}` },
    [base],
  );
}
