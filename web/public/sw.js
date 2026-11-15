/* Itervox hub service worker (#231).
 *
 * Push only: it shows the hub's Web Push notifications and opens their deep
 * link when one is tapped. It caches nothing, so the dashboard is always the
 * one the hub serves. A push payload is minimal by design (kind, project,
 * issue, a short redacted title, a hub-relative link); it never carries agent
 * output, and this file shows nothing beyond it.
 */
self.addEventListener('install', () => {
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(self.clients.claim());
});

function sameOriginPath(url) {
  return typeof url === 'string' && url.startsWith('/') && !url.startsWith('//') ? url : '/hub/m';
}

self.addEventListener('push', (event) => {
  let data = {};
  try {
    data = event.data ? event.data.json() : {};
  } catch {
    data = {};
  }
  const title = typeof data.title === 'string' && data.title ? data.title : 'Itervox';
  const body = [data.project, data.issue].filter((v) => typeof v === 'string' && v).join(' · ');
  event.waitUntil(
    self.registration.showNotification(title, {
      body,
      icon: '/icons/icon-192.png',
      badge: '/icons/icon-192.png',
      tag: data.project && data.issue ? `${data.project}/${data.issue}` : undefined,
      data: { url: sameOriginPath(data.url) },
    }),
  );
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const url = new URL(sameOriginPath(event.notification.data && event.notification.data.url), self.location.origin).href;
  event.waitUntil(
    self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then((windows) => {
      for (const w of windows) {
        if (w.url === url && 'focus' in w) return w.focus();
      }
      return self.clients.openWindow(url);
    }),
  );
});
