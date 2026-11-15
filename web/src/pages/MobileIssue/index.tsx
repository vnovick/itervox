import { useParams } from 'react-router';
import { getApiContext } from '../../auth/apiBase';
import { IssueNowPanel } from '../../components/itervox/changes/IssueNowPanel';
import { IssueSteerBox } from '../../components/itervox/IssueSteerBox';
import { MobileIssueDecision } from '../../components/itervox/hubPhone/MobileIssueDecision';
import { DispatchPauseToggle } from '../Dashboard/components/DispatchPauseToggle';

// #231 — one issue on a phone, inside its project through the hub
// (/hub/p/{project}/m/{issue}): what the run is doing now, the answer it
// waits for (reply, or approve / reject), steering chips, and the project's
// dispatch pause. Every piece is the dashboard's own component over the
// project's proxied API.

export default function MobileIssuePage() {
  const { identifier = '' } = useParams();
  const ctx = getApiContext();
  const project = ctx.mode === 'hubProject' ? ctx.project : '';

  return (
    <main className="mx-auto flex min-h-screen max-w-xl flex-col gap-3 px-4 pt-[max(1rem,env(safe-area-inset-top))] pb-[max(1.5rem,env(safe-area-inset-bottom))]">
      <header className="flex items-center gap-2">
        <a href="/hub/m" className="text-theme-accent-strong text-sm" aria-label="Back to the hub">
          ‹ Hub
        </a>
        <h1 className="text-theme-text font-mono text-base font-semibold">{identifier}</h1>
        {project && <span className="text-theme-muted ml-auto truncate text-xs">{project}</span>}
      </header>
      <MobileIssueDecision identifier={identifier} />
      <IssueNowPanel identifier={identifier} live />
      <IssueSteerBox identifier={identifier} />
      <section aria-label="Project dispatch" className="flex flex-wrap items-center gap-2">
        <DispatchPauseToggle />
      </section>
      {project && (
        <a
          href={`/hub/p/${encodeURIComponent(project)}/?issue=${encodeURIComponent(identifier)}`}
          className="text-theme-muted text-xs underline"
        >
          Open in the full dashboard
        </a>
      )}
    </main>
  );
}
