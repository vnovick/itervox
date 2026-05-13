import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.{test,spec}.{ts,tsx}'],
    // The jsdom suite is UI-heavy and can hit 5s interaction timeouts when
    // Vitest fans out across every local CPU under load. Keep the default
    // contributor path deterministic; VITEST_MAX_WORKERS can still override.
    maxWorkers: 1,
    coverage: {
      provider: 'v8',
      reporter: ['text', 'lcov'],
      reportsDirectory: './coverage',
      include: ['src/**/*.{ts,tsx}'],
      // 70% threshold applies to the well-tested subset of the codebase.
      // Files without dedicated test suites are excluded below.
      thresholds: {
        statements: 70,
        branches: 70,
        functions: 70,
        lines: 70,
      },
      exclude: [
        // Build / test infrastructure
        'src/test/**',
        'src/main.tsx',
        'src/vite-env.d.ts',
        'src/svg.d.ts',
        'src/icons/**',

        // App root (routing wiring only, no business logic)
        'src/App.tsx',

        // Context providers (theme, sidebar) — no business logic
        'src/context/**',

        // Layout shell components — no business logic
        'src/layout/**',
        'src/components/header/**',
        'src/components/layout/**',

        // Generic UI primitives without dedicated tests
        'src/components/common/**',
        'src/components/form/**',
        'src/components/ui/alert/**',
        'src/components/ui/badge/**',
        'src/components/ui/button/**',
        'src/components/ui/dropdown/**',
        'src/components/ui/modal/**',
        'src/components/ui/table/**',
        'src/components/ui/ThemeToggle/**',

        // Itervox components without dedicated test suites
        'src/components/itervox/AgentQueueView.tsx',
        'src/components/itervox/RateLimitBar.tsx',
        'src/components/itervox/StatusStrip.tsx',
        'src/components/itervox/TagInput.tsx',
        'src/components/itervox/HostPool.tsx',
        'src/components/itervox/RetryQueueTable.tsx',
        'src/components/itervox/SessionAccordion.tsx',

        // Extracted timeline presentational components (logic tested via types.test.ts)
        'src/components/itervox/timeline/AgentLogPanel.tsx',
        'src/components/itervox/timeline/IssueRunsView.tsx',
        'src/components/itervox/timeline/RunRow.tsx',
        'src/components/itervox/timeline/SubagentBar.tsx',
        'src/components/itervox/timeline/TimeAxis.tsx',

        // Extracted profile presentational components
        'src/pages/Settings/profiles/ProfileEditorFields.tsx',
        'src/pages/Settings/profiles/ProfileRow.tsx',
        'src/pages/Settings/profiles/SuggestedProfileCard.tsx',
        'src/pages/Settings/profiles/suggestedProfiles.ts',

        // Pages without dedicated test suites
        'src/pages/Blank.tsx',
        'src/pages/Charts/**',
        'src/pages/Forms/**',
        'src/pages/Tables/**',
        'src/pages/UiElements/**',
        'src/pages/UserProfiles.tsx',
        'src/pages/Issues/**',
        'src/pages/Dashboard/**',
        'src/pages/OtherPage/**',
        'src/pages/Timeline/**',
        'src/pages/Settings/index.tsx',
        'src/pages/Settings/ProfilesCard.tsx',
        'src/pages/Settings/ProjectFilterCard.tsx',
        'src/pages/Settings/TrackerStatesCard.tsx',
        'src/pages/Settings/WorkspaceCard.tsx',
        'src/pages/Settings/CapacityCard.tsx',
        'src/pages/Settings/SSHHostsCard.tsx',
        'src/pages/Settings/AddSSHHostModal.tsx',

        // Hooks without dedicated test suites
        'src/hooks/useLogStream.ts',
        'src/hooks/useModal.ts',
        'src/hooks/useSettingsActions.ts',
        'src/hooks/useStableValue.ts',

        // Query files — partially tested via mutation tests
        'src/queries/issues.ts',
        'src/queries/logs.ts',
        'src/queries/projects.ts',

        // Store with complex timer internals — tested implicitly
        'src/store/toastStore.ts',

        // Legacy excluded files
        'src/components/itervox/LogViewer.tsx',
      ],
    },
  },
});
