// Repo Analysis → Prompt versions (#200): per profile, each prompt digest
// (#186) with first/last seen, runs, success rate and average duration,
// newest first. Selecting a version lists its runs, so an operator can see
// whether a SOUL / INSTRUCTIONS / rules change helped.

import { useState } from 'react';
import { usePromptVersions } from '../../../queries/repo';
import { groupPromptVersionsByProfile } from './promptVersionGroups';
import { PromptVersionRuns } from './PromptVersionRuns';
import { PromptVersionTable } from './PromptVersionTable';

export function PromptVersionsSection() {
  const { data, isLoading, isError } = usePromptVersions();
  const [selected, setSelected] = useState<string | null>(null);

  if (isLoading) return <p className="text-theme-muted text-sm">Loading prompt versions…</p>;
  if (isError || !data) {
    return <p className="text-theme-danger-text text-sm">Could not load the prompt versions.</p>;
  }
  const groups = groupPromptVersionsByProfile(data.versions);
  return (
    <div className="space-y-4">
      {groups.length === 0 ? (
        <p className="text-theme-muted text-sm">
          No prompt versions yet — every run dispatched from now on records the digest of its SOUL,
          INSTRUCTIONS, rules and template.
        </p>
      ) : (
        groups.map((g) => (
          <PromptVersionTable
            key={g.profile}
            profile={g.profile}
            versions={g.versions}
            selected={selected}
            onSelect={setSelected}
          />
        ))
      )}
      {data.unversionedRuns > 0 && (
        <p className="text-theme-muted text-xs">
          {data.unversionedRuns} older run{data.unversionedRuns === 1 ? '' : 's'} recorded before
          prompt versions are not shown.
        </p>
      )}
      {selected && (
        <PromptVersionRuns
          digest={selected}
          onClear={() => {
            setSelected(null);
          }}
        />
      )}
    </div>
  );
}
