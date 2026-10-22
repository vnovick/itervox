import { lazy, Suspense } from 'react';
import { getApiContext } from '../../auth/apiBase';

const HubProjectSelect = lazy(() => import('./HubProjectSelect'));

// #228 — in a project dashboard served by the hub (/hub/p/{project}/), switch
// to another project or back to the hub's project list. Renders nothing (and
// loads nothing) on a daemon's own dashboard.
export function HubProjectSwitcher() {
  const ctx = getApiContext();
  if (ctx.mode !== 'hubProject') return null;
  return (
    <Suspense fallback={null}>
      <HubProjectSelect current={ctx.project} />
    </Suspense>
  );
}
