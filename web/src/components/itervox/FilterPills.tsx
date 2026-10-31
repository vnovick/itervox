import { memo } from 'react';

export interface FilterPill {
  id: string;
  label: string;
  /** State names this pill matches. Empty = match all. */
  states: string[];
}

interface FilterPillsProps {
  pills: FilterPill[];
  activeId: string;
  onChange: (id: string) => void;
}

/**
 * Linear-style horizontal filter pills.
 * "All Issues" matches everything; other pills match specific state groups.
 */
export const FilterPills = memo(function FilterPills({
  pills,
  activeId,
  onChange,
}: FilterPillsProps) {
  return (
    // The selected pill gets the chip surface (.segmented-plain).
    <div role="group" aria-label="Filter issues by state" className="segmented segmented-plain">
      {pills.map((pill) => (
        <button
          key={pill.id}
          type="button"
          aria-pressed={activeId === pill.id}
          onClick={() => {
            onChange(pill.id);
          }}
          className="flex-shrink-0 py-1.5 text-[13px]"
        >
          {pill.label}
        </button>
      ))}
    </div>
  );
});
