// Intrinsic sizes of the theme-paired dashboard screenshots in
// public/screenshots/itervox-<name>-<dark|light>.png (both themes share one
// size). Captured from the 0.3.1 dashboard with route-mocked sample data.
export const SHOTS = {
  'live-dashboard': [2200, 1375],
  'kanban-board': [2200, 1159],
  'steer-the-run': [1400, 875],
  'dependency-graph': [1400, 901],
  'review-continue': [1400, 875],
  'system-visibility': [1400, 1138],
  'command-palette': [1400, 875],
  automations: [1400, 1013],
  'new-task': [1400, 875],
  hub: [1400, 331],
  'desktop-app': [1400, 875],
  'phone-dashboard': [640, 1385],
  'hub-phone': [640, 1385],
} as const;

export type ShotName = keyof typeof SHOTS;
