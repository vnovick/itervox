import type { ReactNode } from 'react';
import { buttonClass } from '../ui/button/buttonStyles';

interface TemplateTileProps {
  title: string;
  /** Small pills under the title (backend, model, trigger, profile). */
  meta?: ReactNode;
  /** Shown on hover; the template's dialog repeats it as its subtitle. */
  description?: string;
  /** Shown under the description when the template cannot be used yet. */
  note?: ReactNode;
  disabled?: boolean;
  /** Extra detail shown on hover (e.g. the actions a profile template grants). */
  tooltip?: string;
  onUse: () => void;
}

/**
 * A "start from a template" tile: a compact chip-surface card whose whole
 * face is the button. Shared by the agent profile and automation template
 * grids so both read the same.
 */
export function TemplateTile({
  title,
  meta,
  description,
  note,
  disabled = false,
  tooltip,
  onUse,
}: TemplateTileProps) {
  return (
    <button
      type="button"
      disabled={disabled}
      title={[description, tooltip].filter(Boolean).join('\n') || undefined}
      onClick={onUse}
      className="glass-tile group hover:bg-theme-panel flex h-full w-full flex-col items-start gap-2 px-3 py-2.5 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-60"
    >
      <span className="text-theme-text text-[13px] font-semibold">{title}</span>
      {meta && <span className="flex flex-wrap items-center gap-1">{meta}</span>}
      {description && <span className="sr-only">{description}</span>}
      {note && <span className="text-theme-warning-text text-[11px] leading-snug">{note}</span>}
      <span className={buttonClass('glass', 'xs', 'mt-auto', true)}>Use Template</span>
    </button>
  );
}

/** A tiny neutral pill for a tile's meta row. */
export function TileMeta({ children, mono = false }: { children: ReactNode; mono?: boolean }) {
  return (
    <span className={`pill pill-neutral px-2 py-0 text-[11px] ${mono ? 'font-mono' : ''}`}>
      {children}
    </span>
  );
}
