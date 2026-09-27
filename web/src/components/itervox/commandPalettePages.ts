// CORE-095 — pages reachable from the palette and by g-sequences.
export const PAGES: readonly { path: string; label: string; keys: string }[] = [
  { path: '/', label: 'Dashboard', keys: 'g d' },
  { path: '/logs', label: 'Logs', keys: 'g l' },
  { path: '/timeline', label: 'Timeline', keys: 'g t' },
  { path: '/agents', label: 'Agents', keys: 'g a' },
  { path: '/automations', label: 'Automations', keys: 'g u' },
  { path: '/settings', label: 'Settings', keys: 'g s' },
];
