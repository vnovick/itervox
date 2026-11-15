import { FOCUS_PATH } from '../hooks/useFocusIssue';

/**
 * Every page, in the order of the desktop app's Go menu
 * (desktop/src-tauri/src/menu.rs `go_menu_items`, which a Rust test keeps
 * in step with the route table). Embedded, this menu replaces the sidebar.
 */
export const EMBEDDED_PAGES: readonly { path: string; label: string }[] = [
  { path: '/', label: 'Board' },
  { path: FOCUS_PATH, label: 'Issue view' },
  { path: '/sessions', label: 'Sessions' },
  { path: '/timeline', label: 'Timeline' },
  { path: '/logs', label: 'Logs' },
  { path: '/agents', label: 'Agents' },
  { path: '/automations', label: 'Automations' },
  { path: '/rules', label: 'Rules' },
  { path: '/repo', label: 'Repo Analysis' },
  { path: '/memory', label: 'Memory' },
  { path: '/settings', label: 'Settings' },
];
