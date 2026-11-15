import { memo } from 'react';

interface MobileMenuButtonProps {
  onClick: () => void;
}

/** Opens the navigation drawer. Only visible below the `md` breakpoint. */
export const MobileMenuButton = memo(function MobileMenuButton({ onClick }: MobileMenuButtonProps) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="glass-chip text-theme-text-secondary hover:text-theme-text flex h-9 w-9 shrink-0 items-center justify-center md:hidden"
      aria-label="Open navigation"
      data-testid="mobile-menu-button"
    >
      <svg
        width="18"
        height="18"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        role="img"
        focusable="false"
        aria-hidden="true"
      >
        <path d="M4 7h16M4 12h16M4 17h10" />
      </svg>
    </button>
  );
});
