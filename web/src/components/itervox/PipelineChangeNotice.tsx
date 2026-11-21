import { useShallow } from 'zustand/react/shallow';
import { useItervoxStore } from '../../store/itervoxStore';
import type { LaneReason } from '../../types/schemas';
import { openIssueLanes } from '../../utils/openIssueLanes';
import { LaneReasonList } from './LaneReasonList';

const EMPTY_PATHS: readonly string[] = [];
const EMPTY_REASONS: readonly LaneReason[] = [];
const MAX_SHOWN = 10;

// guardrails.lanes — labels a held push that changes human-lane files
// ("pipeline change") with the files it changes. A held push belongs to a
// running issue (its agent waits in the guardrail shim), so it is rendered
// wherever that approval is shown: the Needs-attention reply row, the issue's
// Now panel ("Waiting for approval"), the input-required panel and the
// phone's approval card. Reads the snapshot's input-required row for the
// issue; renders nothing for any other input request. Below the files it
// says WHY, with file and line (the audit findings on the pushed diff, the
// lane pattern each path matched).
export function PipelineChangeNotice({ identifier }: { identifier: string }) {
  const { paths, total, reasons, reasonsTotal, reasonsUnavailable, lanesView } = useItervoxStore(
    useShallow((s) => {
      const row = s.snapshot?.inputRequired?.find((r) => r.identifier === identifier);
      return {
        paths: row?.guardrailLanePaths ?? EMPTY_PATHS,
        total: row?.guardrailLanePathsTotal ?? 0,
        reasons: row?.guardrailLaneReasons ?? EMPTY_REASONS,
        reasonsTotal: row?.guardrailLaneReasonsTotal ?? 0,
        reasonsUnavailable: row?.guardrailLaneReasonsUnavailable ?? false,
        // The Lanes view lives in the issue's Changes tab (run_context).
        lanesView: s.snapshot?.capabilities?.includes('run_context') ?? false,
      };
    }),
  );
  if (paths.length === 0) return null;
  const shown = paths.slice(0, MAX_SHOWN);
  const more = Math.max(total, paths.length) - shown.length;

  return (
    <div className="space-y-1" data-testid="pipeline-change-notice">
      <span className="border-theme-warning/40 text-theme-warning-text inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-semibold">
        Pipeline change
      </span>
      <p className="text-theme-text-secondary text-xs">
        This push changes how CI checks the work. Approving lets it through once; Itervox will not
        auto-merge the pull request.
      </p>
      {/* break-all: a long workflow path wraps on a phone instead of overflowing. */}
      <ul
        className="text-theme-text-secondary font-mono text-xs break-all"
        aria-label="Pipeline files changed"
      >
        {shown.map((p) => (
          <li key={p}>{p}</li>
        ))}
      </ul>
      {more > 0 && <p className="text-theme-muted text-xs">and {more} more</p>}
      {reasonsUnavailable ? (
        <p className="text-theme-muted text-xs" data-testid="lane-reasons-unavailable">
          Why this needs a person: reasons unavailable (Itervox could not recompute them from the
          worktree).
        </p>
      ) : (
        <LaneReasonList reasons={reasons} total={reasonsTotal} />
      )}
      {lanesView && (
        <button
          type="button"
          className="text-theme-accent-text text-xs underline-offset-2 hover:underline"
          onClick={() => {
            openIssueLanes(identifier);
          }}
        >
          Open the Lanes view
        </button>
      )}
    </div>
  );
}
