import { useTheme } from '../../../context/ThemeContext';

/**
 * The sidebar's labelled theme switch. The visible label is always "Theme";
 * the accessible name adds the action ("Theme: switch to light mode"), so
 * the visible text stays part of the name (WCAG 2.5.3).
 */
export function ThemeToggle() {
  const { theme, toggleTheme } = useTheme();
  const next = theme === 'dark' ? 'light' : 'dark';

  return (
    <button
      type="button"
      onClick={toggleTheme}
      aria-label={`Theme: switch to ${next} mode`}
      data-theme-current={theme}
      className="glass-chip text-theme-text-secondary hover:text-theme-text flex min-h-11 w-full items-center gap-2.5 rounded-[14px] px-3 text-[15px]"
    >
      <svg
        width="18"
        height="18"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden="true"
      >
        {theme === 'dark' ? (
          <path d="M12 16a4 4 0 100-8 4 4 0 000 8zM12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
        ) : (
          <path d="M21 12.8A9 9 0 1111.2 3a7 7 0 009.8 9.8z" />
        )}
      </svg>
      Theme
    </button>
  );
}
