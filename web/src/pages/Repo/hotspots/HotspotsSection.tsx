// Repo Analysis → Hotspots (#201): files and directories ranked by how often
// agent work on them went wrong — failed or stalled runs, reviewer change
// requests, input requests and reverted agent PRs — from GET
// /api/v1/repo/hotspots. The server computes it in a background job,
// refreshed at most every 10 minutes; this section polls while it runs.

import { useState } from 'react';
import { useRepoHotspots } from '../../../queries/repo';
import type { RepoHotspotRow as Row } from '../../../types/schemas';
import { fmtSeen } from '../prompts/promptVersionGroups';
import { HotspotRow } from './HotspotRow';

const EMPTY_ROWS: readonly Row[] = [];
type View = 'files' | 'directories';

export function HotspotsSection() {
  const { data, isLoading, isError } = useRepoHotspots();
  const [view, setView] = useState<View>('files');
  const rows = (view === 'files' ? data?.files : data?.directories) ?? EMPTY_ROWS;
  const computed = Boolean(data?.generatedAt);

  return (
    <div className="border-theme-line bg-theme-panel rounded-lg border p-4">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
        <div>
          <p className="text-theme-text text-sm font-medium">Failure hotspots</p>
          <p className="text-theme-muted text-[11px]">
            Where agent runs went wrong, by the files their branch changed.
            {data &&
              computed &&
              ` ${String(data.runsWithSignals)} of ${String(data.runsConsidered)} runs went wrong; ${String(data.runsAttributed)} attributed to files.`}
            {data?.refreshing && ' Refreshing…'}
          </p>
        </div>
        <div role="group" aria-label="Hotspot view" className="flex gap-1">
          {(['files', 'directories'] as const).map((v) => (
            <button
              key={v}
              type="button"
              aria-pressed={view === v}
              onClick={() => {
                setView(v);
              }}
              className={`border-theme-line rounded border px-2 py-1 text-[11px] ${
                view === v ? 'text-theme-text bg-[var(--bg-soft)]' : 'text-theme-muted'
              }`}
            >
              {v === 'files' ? 'Files' : 'Directories'}
            </button>
          ))}
        </div>
      </div>
      {isLoading && <p className="text-theme-muted text-xs">Loading hotspots…</p>}
      {isError && <p className="text-theme-danger-text text-xs">Could not load the hotspots.</p>}
      {data && !computed && (
        <p className="text-theme-muted text-xs">Computing hotspots from the run history and git…</p>
      )}
      {data && computed && rows.length === 0 && (
        <p className="text-theme-muted text-xs">
          No hotspots: no run in the history window went wrong on files this repository can see.
        </p>
      )}
      {rows.length > 0 && (
        <ul
          className="space-y-2"
          aria-label={view === 'files' ? 'File hotspots' : 'Directory hotspots'}
        >
          {rows.map((r) => (
            <HotspotRow key={r.path} row={r} />
          ))}
        </ul>
      )}
      {(data?.notes ?? []).length > 0 && (
        <ul
          className="text-theme-muted mt-3 list-disc space-y-0.5 pl-5 text-[11px]"
          aria-label="Hotspot notes"
        >
          {(data?.notes ?? []).map((n) => (
            <li key={n}>{n}</li>
          ))}
        </ul>
      )}
      {data?.generatedAt && (
        <p className="text-theme-muted mt-3 text-[10px]">
          Computed {fmtSeen(data.generatedAt)}
          {data.repo && ` from ${data.repo}`}
          {data.baseRef && ` against ${data.baseRef}`}; refreshed at most every 10 minutes.
        </p>
      )}
    </div>
  );
}
