import { useEffect } from 'react';
import { useToastStore } from '../store/toastStore';

const CHANNEL_NAME = 'itervox-tab-sync';
const PING = 'ping';
const PONG = 'pong';

// CORE-099 — measured (M6-W2, Chrome over plain HTTP/1.1): each tab holds 2
// live streams on the Dashboard and Logs pages and 3 on Timeline; browsers
// allow 6 connections per server, so a third Dashboard tab (or a second
// Timeline tab) exhausts them and further requests stall. Informational: the
// tab still works, and closing one frees its connections.
export const MULTI_TAB_NOTICE =
  'Itervox is open in another tab. Browsers allow 6 connections per server and each tab keeps 2–3 open, so a third tab can stall page loads. Close tabs you are not using.';

/**
 * Detects when multiple browser tabs/windows have Itervox open and shows a
 * warning toast. Uses BroadcastChannel to coordinate — no server changes needed.
 *
 * Flow: on mount, sends a PING. Any existing tab replies with PONG.
 * If we receive a PONG, show a warning. If we receive a PING, reply with PONG
 * (the other tab will show the warning).
 */
export function useMultiTabWarning() {
  useEffect(() => {
    if (typeof BroadcastChannel === 'undefined') return; // SSR / unsupported browser

    const channel = new BroadcastChannel(CHANNEL_NAME);
    let warned = false;

    channel.onmessage = (event: MessageEvent) => {
      if (event.data === PING) {
        // Another tab just opened — reply so it knows we exist.
        channel.postMessage(PONG);
      } else if (event.data === PONG && !warned) {
        // Another tab replied to our ping — we're the new tab, show warning.
        warned = true;
        useToastStore.getState().addToast(MULTI_TAB_NOTICE, 'info');
      }
    };

    // Announce ourselves — any existing tab will reply with PONG.
    channel.postMessage(PING);

    return () => {
      channel.close();
    };
  }, []);
}
