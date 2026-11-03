import { useCallback, useEffect, useId, useRef, useState, type KeyboardEvent } from 'react';
import { Link, useLocation, useMatch } from 'react-router';
import { useItervoxStore } from '../store/itervoxStore';
import { useFocusIssue } from '../hooks/useFocusIssue';
import { EMBEDDED_PAGES } from './embeddedPages';

const CHIP_LINK =
  'glass-chip text-theme-text hover:bg-theme-panel-strong aria-[current=page]:bg-theme-card inline-flex h-9 shrink-0 items-center px-3.5 text-[13px] font-medium transition-colors';

/** Embedded top bar title: `project · ISSUE` (the issue on the issue view). */
export function EmbeddedTitle() {
  const projectName = useItervoxStore((s) => s.snapshot?.projectName ?? '');
  const focus = useFocusIssue();
  return (
    <span
      data-testid="embedded-title"
      className="text-theme-text min-w-0 truncate text-[15px] font-bold sm:text-base"
    >
      {projectName || 'Itervox'}
      {focus && (
        <span className="text-theme-muted font-mono text-sm font-medium">
          {' · '}
          {focus.identifier}
        </span>
      )}
    </span>
  );
}

/** A top bar pill link, current when its route matches exactly. */
function ChipLink({ to, label }: { to: string; label: string }) {
  const match = useMatch({ path: to, end: true });
  return (
    <Link to={to} aria-current={match ? 'page' : undefined} className={CHIP_LINK}>
      {label}
    </Link>
  );
}

/** Embedded top bar navigation: Board and Sessions; the rest under More. */
export function EmbeddedNavLinks() {
  return (
    <nav aria-label="Pages" className="flex items-center gap-2">
      <ChipLink to="/" label="Board" />
      <span className="hidden sm:contents">
        <ChipLink to="/sessions" label="Sessions" />
      </span>
    </nav>
  );
}

/**
 * The "More" menu: every page (EMBEDDED_PAGES). A menu button: Arrow keys
 * move between items, Escape closes and returns focus to the button, a
 * click outside or following an item closes it.
 */
export function EmbeddedMoreMenu() {
  const [open, setOpen] = useState(false);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLUListElement>(null);
  const menuId = useId();
  const { pathname } = useLocation();

  const close = useCallback((refocus: boolean) => {
    setOpen(false);
    if (refocus) buttonRef.current?.focus();
  }, []);

  useEffect(() => {
    if (!open) return;
    menuRef.current?.querySelector<HTMLElement>('[role="menuitem"]')?.focus();
    const onPointer = (e: PointerEvent) => {
      const t = e.target as Node;
      if (!menuRef.current?.contains(t) && !buttonRef.current?.contains(t)) close(false);
    };
    document.addEventListener('pointerdown', onPointer);
    return () => {
      document.removeEventListener('pointerdown', onPointer);
    };
  }, [open, close]);

  const onMenuKey = (e: KeyboardEvent<HTMLUListElement>) => {
    const items = Array.from(
      menuRef.current?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? [],
    );
    const at = items.indexOf(document.activeElement as HTMLElement);
    const move = (i: number) => {
      e.preventDefault();
      items[(i + items.length) % items.length]?.focus();
    };
    if (e.key === 'ArrowDown') move(at + 1);
    else if (e.key === 'ArrowUp') move(at - 1);
    else if (e.key === 'Home') move(0);
    else if (e.key === 'End') move(items.length - 1);
    else if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      close(true);
    } else if (e.key === 'Tab') close(false);
  };

  return (
    <div className="relative shrink-0">
      <button
        ref={buttonRef}
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={() => {
          setOpen((v) => !v);
        }}
        onKeyDown={(e) => {
          if (e.key === 'ArrowDown' && !open) {
            e.preventDefault();
            setOpen(true);
          }
        }}
        className={CHIP_LINK}
      >
        More
        <svg
          aria-hidden="true"
          viewBox="0 0 24 24"
          className="ml-1 h-3.5 w-3.5"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
          strokeLinecap="round"
        >
          <path d="m6 9 6 6 6-6" />
        </svg>
      </button>
      {open && (
        <ul
          ref={menuRef}
          id={menuId}
          role="menu"
          aria-label="All pages"
          onKeyDown={onMenuKey}
          className="glass-sheet absolute top-full right-0 z-50 mt-2 flex w-52 flex-col gap-0.5 p-1.5"
        >
          {EMBEDDED_PAGES.map((p) => {
            const current =
              p.path === '/'
                ? pathname === '/'
                : pathname === p.path || pathname.startsWith(`${p.path}/`);
            return (
              <li key={p.path} role="none">
                <Link
                  role="menuitem"
                  to={p.path}
                  aria-current={current ? 'page' : undefined}
                  data-active={current ? 'true' : undefined}
                  className="nav-item"
                  onClick={() => {
                    close(false);
                  }}
                >
                  {p.label}
                </Link>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
