import { useShallow } from 'zustand/react/shallow';
import { useItervoxStore } from '../../store/itervoxStore';

const EMPTY_PATHS: readonly string[] = [];
const MAX_SHOWN = 10;

// guardrails.lanes — labels a held push that changes human-lane files
// ("pipeline change") with the files it changes, inside the input-required
// panel. Reads the snapshot's input-required row for the issue; renders
// nothing for any other input request.
export function PipelineChangeNotice({ identifier }: { identifier: string }) {
  const { paths, total } = useItervoxStore(
    useShallow((s) => {
      const row = s.snapshot?.inputRequired?.find((r) => r.identifier === identifier);
      return {
        paths: row?.guardrailLanePaths ?? EMPTY_PATHS,
        total: row?.guardrailLanePathsTotal ?? 0,
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
      <ul
        className="text-theme-text-secondary font-mono text-xs"
        aria-label="Pipeline files changed"
      >
        {shown.map((p) => (
          <li key={p}>{p}</li>
        ))}
      </ul>
      {more > 0 && <p className="text-theme-muted text-xs">and {more} more</p>}
    </div>
  );
}
