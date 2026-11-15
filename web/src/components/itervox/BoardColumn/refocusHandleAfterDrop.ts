/**
 * After a keyboard drop into another column the card re-mounts there, and
 * when its old handle unmounts focus falls to <body>. dnd-kit's own focus
 * restore runs one frame after the drop, which is before the re-mounted card
 * exists whenever rendering is slow, so it can miss. This follows the card
 * for a few frames: while focus is on <body> it puts it on the card's
 * handle; it stops as soon as focus is somewhere else (the user moved on).
 */
export function refocusHandleAfterDrop(identifier: string, frames = 30): void {
  const selector = `[data-drag-handle-for="${CSS.escape(identifier)}"]`;
  let left = frames;
  const tick = () => {
    const active = document.activeElement;
    const onBody = !active || active === document.body;
    const onOwnHandle = active instanceof HTMLElement && active.matches(selector);
    if (!onBody && !onOwnHandle) return;
    if (onBody) document.querySelector<HTMLElement>(selector)?.focus();
    left -= 1;
    if (left > 0) requestAnimationFrame(tick);
  };
  requestAnimationFrame(tick);
}
