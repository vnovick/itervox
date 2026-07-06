// Re-exports of the shared scenarios from `web/src/test/fixtures/scenarios.ts`
// adapted for Playwright. The shape is the same; this barrel exists so e2e
// specs don't need to reach into `src/test/...` and so the import path is
// uniform across the lane.

export {
  activeRunScenario,
  automationsPassScenario,
  configInvalidScenario,
  emptyScenario,
  inputRequiredScenario,
  mobileShellScenario,
  quickstartScenario,
  retryAndPausedScenario,
  settingsMatrixScenario,
  timelineLogsScenario,
  notificationsScenario,
  type Scenario,
} from '../../src/test/fixtures/scenarios';

export { E2E_TOKEN } from '../../src/test/fixtures/auth';
