type BadgeVariant = 'light' | 'solid';
type BadgeSize = 'sm' | 'md';
type BadgeColor = 'primary' | 'success' | 'error' | 'warning' | 'info' | 'light' | 'dark';

interface BadgeProps {
  variant?: BadgeVariant;
  size?: BadgeSize;
  color?: BadgeColor;
  startIcon?: React.ReactNode;
  endIcon?: React.ReactNode;
  children: React.ReactNode;
}

// Maps semantic color names to control-room CSS tokens
function badgeStyle(variant: BadgeVariant, color: BadgeColor): React.CSSProperties {
  if (variant === 'solid') {
    const bg: Record<BadgeColor, string> = {
      primary: 'var(--accent)',
      success: 'var(--success)',
      error: 'var(--danger)',
      warning: 'var(--warning)',
      info: 'var(--teal)',
      light: 'var(--bg-soft)',
      dark: 'var(--panel-strong)',
    };
    return { background: bg[color], color: '#fff' };
  }
  // light variant
  const styles: Record<BadgeColor, React.CSSProperties> = {
    primary: { background: 'var(--accent-soft)', color: 'var(--accent-strong)' },
    success: { background: 'var(--success-soft)', color: 'var(--success-text)' },
    error: { background: 'var(--danger-soft)', color: 'var(--danger-text)' },
    warning: { background: 'var(--warning-soft)', color: 'var(--warning-text)' },
    info: { background: 'var(--teal-soft)', color: 'var(--teal)' },
    light: { background: 'var(--bg-soft)', color: 'var(--text-secondary)' },
    dark: { background: 'var(--panel-strong)', color: 'var(--text)' },
  };
  return styles[color];
}

const Badge: React.FC<BadgeProps> = ({
  variant = 'light',
  color = 'primary',
  size = 'md',
  startIcon,
  endIcon,
  children,
}) => {
  return (
    <span
      className={`inline-flex items-center justify-center gap-1 rounded-full font-medium ${
        size === 'sm' ? 'px-2 py-0.5 text-xs' : 'px-2.5 py-0.5 text-sm'
      }`}
      style={badgeStyle(variant, color)}
    >
      {startIcon && <span className="mr-1">{startIcon}</span>}
      {children}
      {endIcon && <span className="ml-1">{endIcon}</span>}
    </span>
  );
};

export default Badge;
