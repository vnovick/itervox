import { clsx, type ClassValue } from 'clsx';
import { twMerge } from 'tailwind-merge';

/**
 * CORE-094 — class composition for the UI primitives: clsx for conditional
 * classes, tailwind-merge so a caller's `className` overrides a primitive's
 * default of the same utility group (`px-4` replaces `px-2`) instead of both
 * landing in the class list with CSS source order deciding.
 */
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
