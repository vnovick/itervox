import { useDebouncedValue } from '../../../hooks/useDebouncedValue';
import { useFilterPreview } from '../../../queries/trackerScope';
import type { FilterGroup } from '../../../types/schemas';

// #198 — the live preview: how many issues in the active states the draft
// filter reads now (POST /api/v1/tracker/filter/preview, read-only),
// re-asked once the draft has been still for PREVIEW_DEBOUNCE_MS.

export const PREVIEW_DEBOUNCE_MS = 400;

interface Props {
  groups: FilterGroup[];
  /** False while the draft is not a valid filter (an empty group). */
  valid: boolean;
}

export function FilterPreviewLine({ groups, valid }: Props) {
  const settled = useDebouncedValue(groups, PREVIEW_DEBOUNCE_MS);
  const stale = settled !== groups;
  const { data, isFetching, isError, error } = useFilterPreview(settled, valid);

  let text: string;
  if (!valid) text = 'Preview: finish every group to see how many issues match.';
  else if (isError)
    text = `Preview failed: ${error instanceof Error ? error.message : 'unknown error'}`;
  else if (!data || stale || isFetching) text = 'Preview: counting matching issues…';
  else {
    const sample =
      data.identifiers.length > 0
        ? ` (${data.identifiers.join(', ')}${data.count > data.identifiers.length ? ', …' : ''})`
        : '';
    text = `Preview: ${String(data.count)} issue${data.count === 1 ? '' : 's'} in the active states match${sample}.`;
  }
  return (
    <p
      data-testid="filter-preview"
      aria-live="polite"
      className="text-theme-text-secondary text-xs"
    >
      {text}
    </p>
  );
}
