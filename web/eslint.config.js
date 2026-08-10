import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'
import { defineConfig, globalIgnores } from 'eslint/config'

export default defineConfig([
  globalIgnores(['dist']),
  {
    files: ['**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      tseslint.configs.recommended,
      reactHooks.configs.flat.recommended,
      reactRefresh.configs.vite,
    ],
    languageOptions: {
      globals: globals.browser,
    },
    rules: {
      // Several controlled UI states intentionally mirror query/prop changes. React's
      // blanket advisory rejects those synchronization effects even though they do not
      // form render loops.
      'react-hooks/set-state-in-effect': 'off',
    },
  },
  {
    // Entry points, generated shadcn primitives, and the shared priority module export
    // non-component helpers by design; they are not hot-reload boundaries.
    files: ['src/main.tsx', 'src/components/ui/**/*.tsx', 'src/components/PriorityIcon.tsx'],
    rules: {
      'react-refresh/only-export-components': 'off',
    },
  },
])
