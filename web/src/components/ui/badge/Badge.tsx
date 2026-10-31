type BadgeVariant = 'light' | 'solid' | 'glass';
type BadgeSize = 'sm' | 'md';
type BadgeColor = 'primary' | 'success' | 'error' | 'warning' | 'info' | 'light' | 'dark';

interface BadgeProps {
  /** `light` = soft tint of the colour (default), `solid` = filled, `glass` = glass chip. */
  variant?: BadgeVariant;
  size?: BadgeSize;
  color?: BadgeColor;
  startIcon?: React.ReactNode;
  endIcon?: React.ReactNode;
  children: React.ReactNode;
}

// Soft tints reuse the `.pill-*` tone classes from index.css.
const LIGHT_CLASS: Record<BadgeColor, string> = {
  primary: 'pill-accent',
  success: 'pill-ok',
  error: 'pill-danger',
  warning: 'pill-warn',
  info: 'bg-theme-teal-soft text-theme-text',
  light: 'pill-neutral',
  dark: 'bg-theme-panel-strong text-theme-text',
};

const SOLID_BG: Record<BadgeColor, string> = {
  primary: 'var(--accent)',
  success: 'var(--success)',
  error: 'var(--danger)',
  warning: 'var(--warning)',
  info: 'var(--teal)',
  light: 'var(--bg-soft)',
  dark: 'var(--panel-strong)',
};

/** A status pill (radius 999). */
const Badge: React.FC<BadgeProps> = ({
  variant = 'light',
  color = 'primary',
  size = 'md',
  startIcon,
  endIcon,
  children,
}) => {
  const tone =
    variant === 'solid'
      ? 'text-white'
      : variant === 'glass'
        ? 'glass-chip text-theme-text'
        : LIGHT_CLASS[color];
  return (
    <span
      className={`pill justify-center ${tone} ${size === 'sm' ? 'px-2 py-0.5 text-xs' : ''}`}
      style={variant === 'solid' ? { background: SOLID_BG[color] } : undefined}
    >
      {startIcon && <span className="mr-1">{startIcon}</span>}
      {children}
      {endIcon && <span className="ml-1">{endIcon}</span>}
    </span>
  );
};

export default Badge;
