/** The agent backends the daemon accepts (server config.IsSupportedBackend). */
const KNOWN_BACKENDS = ['claude', 'codex'] as const;
const BACKEND_LABELS: Record<(typeof KNOWN_BACKENDS)[number], string> = {
  claude: 'Claude',
  codex: 'Codex',
};

interface BackendSelectorProps {
  value: string;
  onChange: (backend: string) => void;
  /** When true, renders a read-only badge instead of a dropdown. */
  readOnly?: boolean;
  /** Show "Backend:" label. Default true. */
  showLabel?: boolean;
  /** `field`: a full-width form select (glass chip, 40px). */
  size?: 'sm' | 'md' | 'field';
  /**
   * When set, adds a first option with value "" (clear the override) and
   * this label, e.g. "Default (claude)". CORE-056.
   */
  defaultOptionLabel?: string;
  /** Accessible name when the visible "Backend:" label is hidden. */
  ariaLabel?: string;
  /** Links the select to an external description (help / error text). */
  ariaDescribedBy?: string;
  disabled?: boolean;
}

const SIZE_CLS = {
  sm: 'px-1.5 py-0.5 text-[10px]',
  md: 'px-2.5 py-1.5 text-xs',
  field: 'min-h-10 w-full min-w-0 px-3 text-[13px]',
} as const;

// `field` matches the other full-width selects of a form row (the issue
// detail's profile / playbook / backend row): the glass chip surface.
const SURFACE_CLS = {
  sm: 'border-theme-line bg-theme-panel-strong border font-medium',
  md: 'border-theme-line bg-theme-panel-strong border font-medium',
  field: 'glass-chip',
} as const;

export function BackendSelector({
  value,
  onChange,
  readOnly = false,
  showLabel = true,
  size = 'md',
  defaultOptionLabel,
  ariaLabel,
  ariaDescribedBy,
  disabled = false,
}: BackendSelectorProps) {
  if (readOnly) {
    return (
      <span
        className={`bg-theme-bg-soft text-theme-text-secondary rounded-full font-medium ${SIZE_CLS[size]}`}
      >
        {value}
      </span>
    );
  }

  const select = (
    <select
      value={value}
      aria-label={ariaLabel}
      aria-describedby={ariaDescribedBy}
      disabled={disabled}
      onChange={(e) => {
        onChange(e.target.value);
      }}
      onClick={(e) => {
        e.stopPropagation();
      }}
      className={`${SURFACE_CLS[size]} text-theme-text focus-visible:ring-theme-accent cursor-pointer rounded-[var(--radius-sm)] focus:outline-none focus-visible:ring-2 disabled:cursor-not-allowed disabled:opacity-60 ${SIZE_CLS[size]}`}
    >
      {defaultOptionLabel !== undefined && <option value="">{defaultOptionLabel}</option>}
      {KNOWN_BACKENDS.map((b) => (
        <option key={b} value={b}>
          {BACKEND_LABELS[b]}
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
        Backend:
        {select}
      </label>
    </span>
  );
}
