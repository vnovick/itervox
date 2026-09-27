import { useEffect, useId, useRef, useState, useSyncExternalStore, type RefObject } from 'react';
import { useFocusTrap } from '../../../hooks/useFocusTrap';
import {
  acquireScrollLock,
  pushLayer,
  releaseScrollLock,
  removeLayer,
  subscribeLayers,
  topLayerId,
} from './overlayStack';

interface DialogLayerOptions {
  isOpen: boolean;
  onClose: () => void;
  containerRef: RefObject<HTMLElement | null>;
  /** Lock body scroll while open. Default true. */
  lockScroll?: boolean;
}

/**
 * The shared dialog primitive behind SlidePanel, Modal and the mobile nav
 * drawer (CORE-067). While `isOpen`:
 *  - registers the dialog on the overlay stack, so Escape closes only the
 *    top-most dialog;
 *  - traps focus in `containerRef` only while this dialog is on top, moves
 *    focus in on open and restores it to the opener on close;
 *  - holds a ref-counted body scroll lock (idempotent per dialog).
 *
 * Returns whether this dialog is currently the top-most layer.
 */
export function useDialogLayer({
  isOpen,
  onClose,
  containerRef,
  lockScroll = true,
}: DialogLayerOptions): boolean {
  const id = useId();
  // M5-close — capture the opener while RENDERING the open transition, i.e.
  // before this commit mounts the dialog's children. An `autoFocus` child is
  // focused during that commit, before any effect (layout or passive) of this
  // hook runs, so reading document.activeElement in an effect records the
  // dialog's own input and focus fell to <body> on close. This is React's
  // "adjust state while rendering" pattern (it re-renders before commit).
  const [opener, setOpener] = useState<HTMLElement | null>(() => (isOpen ? activeElement() : null));
  const [wasOpen, setWasOpen] = useState(isOpen);
  if (isOpen !== wasOpen) {
    setWasOpen(isOpen);
    setOpener(isOpen ? activeElement() : null);
  }
  const onCloseRef = useRef(onClose);
  useEffect(() => {
    onCloseRef.current = onClose;
  }, [onClose]);

  useEffect(() => {
    if (!isOpen) return;
    pushLayer(id, () => {
      onCloseRef.current();
    });
    return () => {
      removeLayer(id);
    };
  }, [id, isOpen]);

  useEffect(() => {
    if (!isOpen || !lockScroll) return;
    acquireScrollLock(id);
    return () => {
      releaseScrollLock(id);
    };
  }, [id, isOpen, lockScroll]);

  const isTop = useSyncExternalStore(subscribeLayers, () => topLayerId() === id);

  useFocusTrap(containerRef, isOpen, { active: isTop, opener });

  return isTop;
}

function activeElement(): HTMLElement | null {
  if (typeof document === 'undefined') return null;
  const el = document.activeElement;
  return el instanceof HTMLElement && el !== document.body ? el : null;
}
