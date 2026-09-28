import { useAttentionNotifications } from '../../hooks/useAttentionNotifications';

/** CORE-096 — the one app-wide watcher; renders nothing. */
export function AttentionNotifier() {
  useAttentionNotifications();
  return null;
}
