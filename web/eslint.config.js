import js from '@eslint/js';
import globals from 'globals';
import reactHooks from 'eslint-plugin-react-hooks';
import reactRefresh from 'eslint-plugin-react-refresh';
import jsxA11y from 'eslint-plugin-jsx-a11y';
import tseslint from 'typescript-eslint';

export default tseslint.config(
  { ignores: ['dist', 'node_modules', 'public'] },
  {
    extends: [js.configs.recommended, ...tseslint.configs.strictTypeChecked],
    files: ['src/**/*.{ts,tsx}'],
    languageOptions: {
      ecmaVersion: 2020,
      globals: globals.browser,
      parserOptions: {
        project: ['./tsconfig.app.json'],
        tsconfigRootDir: import.meta.dirname,
      },
    },
    plugins: {
      'react-hooks': reactHooks,
      'react-refresh': reactRefresh,
      // CORE-088 — static a11y checks (the Playwright axe spec covers runtime).
      'jsx-a11y': jsxA11y,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      ...jsxA11y.flatConfigs.recommended.rules,
      // The card-style checkbox labels nest their text three levels deep
      // (label > span > span); the rule's default depth of 2 misses it.
      'jsx-a11y/label-has-associated-control': ['error', { depth: 3 }],
      'react-refresh/only-export-components': ['warn', { allowConstantExport: true }],
      '@typescript-eslint/consistent-type-imports': ['error', { prefer: 'type-imports' }],
      '@typescript-eslint/no-floating-promises': 'error',
      '@typescript-eslint/no-explicit-any': 'error',
      '@typescript-eslint/explicit-module-boundary-types': 'off',
      '@typescript-eslint/no-misused-promises': [
        'error',
        { checksVoidReturn: { attributes: false } },
      ],
      'no-restricted-imports': [
        'error',
        {
          patterns: [
            {
              group: ['**/types/itervox'],
              message:
                "Import from '../types/schemas' or '../types/itervox-types' instead — types/itervox.ts is @deprecated.",
            },
          ],
        },
      ],
      // #298 — a raw WebSocket cannot carry the bearer token: the live
      // terminal's socket (ticket-authenticated) is the only one, in
      // src/auth/terminalSocket.ts (exempted below). Everything else uses
      // authedFetch / openAuthedEventStream.
      'no-restricted-globals': [
        'error',
        {
          name: 'WebSocket',
          message:
            'Open sockets only through src/auth/terminalSocket.ts (ticket auth, #298); HTTP goes through authedFetch.',
        },
      ],
    },
  },
  {
    // The one raw WebSocket (#298): the ticketed live-terminal socket.
    files: ['src/auth/terminalSocket.ts'],
    rules: { 'no-restricted-globals': 'off' },
  },
  {
    files: ['src/**/*.{test,spec}.{ts,tsx}', 'src/test/**/*.{ts,tsx}'],
    languageOptions: {
      parserOptions: {
        project: ['./tsconfig.test.json'],
        tsconfigRootDir: import.meta.dirname,
      },
    },
    rules: {
      '@typescript-eslint/no-explicit-any': 'off',
      '@typescript-eslint/no-unsafe-assignment': 'off',
      '@typescript-eslint/no-unsafe-call': 'off',
      '@typescript-eslint/no-unsafe-member-access': 'off',
    },
  },
);
