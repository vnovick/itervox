import type { HTMLAttributes } from 'react';
import { cn } from '../cn';

export type BannerTone = 'info' | 'success' | 'warning' | 'danger';

const TONE: Record<BannerTone, string> = {
  info: 'bg-theme-accent-soft text-theme-accent-strong',
  success: 'bg-theme-success-soft text-theme-success-text',
  warning: 'bg-theme-warning-soft text-theme-warning-text',
  danger: 'bg-theme-danger-soft text-theme-danger-text',
};

export interface BannerProps extends HTMLAttributes<HTMLDivElement> {
  tone?: BannerTone;
}

/**
 * CORE-094 — an inline notice. `danger` is announced assertively
 * (role=alert); the other tones politely (role=status).
 */
export function Banner({ tone = 'info', className, ...rest }: BannerProps) {
  return (
    <div
      role={tone === 'danger' ? 'alert' : 'status'}
      className={cn('px-4 py-1.5 text-xs', TONE[tone], className)}
      {...rest}
    />
  );
}
