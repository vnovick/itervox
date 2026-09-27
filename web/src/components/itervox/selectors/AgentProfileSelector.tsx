import { EMPTY_PROFILE_LABEL } from '../../../utils/format';

interface AgentProfileSelectorProps {
  value: string;
  availableProfiles: string[];
  onChange: (profile: string) => void;
  /** Show "Agent:" label. Default true. */
  showLabel?: boolean;
  size?: 'sm' | 'md';
}

const SIZE_CLS = {
  sm: 'px-1.5 py-0.5 text-[10px]',
  md: 'px-2.5 py-1.5 text-xs',
} as const;

export function AgentProfileSelector({
  value,
  availableProfiles,
  onChange,
  showLabel = true,
  size = 'md',
}: AgentProfileSelectorProps) {
  if (availableProfiles.length === 0) return null;

  const select = (
    <select
      value={value}
      onChange={(e) => {
        onChange(e.target.value);
      }}
      onClick={(e) => {
        e.stopPropagation();
      }}
      className={`border-theme-line bg-theme-panel-strong text-theme-text focus:border-theme-accent cursor-pointer rounded-[var(--radius-sm)] border font-medium focus:outline-none ${SIZE_CLS[size]}`}
    >
      <option value="">{EMPTY_PROFILE_LABEL}</option>
      {availableProfiles.map((p) => (
        <option key={p} value={p}>
          {p}
        </option>
      ))}
    </select>
  );

  if (!showLabel) return select;

  return (
    // Event boundary only: keeps clicks on the picker from reaching the row.
    <span
      role="presentation"
      className="flex-shrink-0"
      onClick={(e) => {
        e.stopPropagation();
      }}
    >
      <label className="text-theme-muted flex items-center gap-1 text-[10px]">
        Agent:
        {select}
      </label>
    </span>
  );
}
