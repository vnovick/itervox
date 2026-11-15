import { Select } from '../../../components/ui/Select';
import { shortDigest } from '../../../utils/promptVersions';
import type { PromptVersionSummary } from '../../../utils/promptVersions';

interface PromptVersionFilterProps {
  versions: readonly PromptVersionSummary[];
  value: string;
  onChange: (digest: string) => void;
}

/**
 * #186 — narrows the timeline to the runs of one prompt version and shows,
 * per version, how many runs it had and how many succeeded.
 */
export function PromptVersionFilter({ versions, value, onChange }: PromptVersionFilterProps) {
  return (
    <label className="glass-chip text-theme-text flex min-h-10 items-center gap-2 pl-3.5 text-[13px]">
      Prompt version
      <Select
        data-testid="timeline-prompt-version"
        aria-label="Filter runs by prompt version"
        className="h-8 w-auto rounded-full border-0 bg-transparent py-0 font-semibold"
        value={value}
        disabled={versions.length === 0}
        title={
          versions.length === 0
            ? 'No run in history recorded a prompt version yet'
            : 'Show only the runs of one prompt version'
        }
        onChange={(e) => {
          onChange(e.target.value);
        }}
      >
        <option value="">all</option>
        {versions.map((v) => (
          <option key={v.digest} value={v.digest}>
            {`${shortDigest(v.digest)} · ${String(v.runs)} run${v.runs === 1 ? '' : 's'} · ${String(Math.round(v.successRate * 100))}% succeeded`}
          </option>
        ))}
      </Select>
    </label>
  );
}
