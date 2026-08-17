
import js from '@eslint/js'
import globals from 'globals'
import svelte from 'eslint-plugin-svelte'
import svelteParser from 'svelte-eslint-parser'
import tseslint from 'typescript-eslint'

const noHtmlSinks = {
  'no-restricted-syntax': [
    'error',
    { selector: "MemberExpression[property.name='innerHTML']", message: 'Build nodes instead. A page rendering account data must never parse markup.' },
    { selector: "MemberExpression[property.name='outerHTML']", message: 'Build nodes instead. A page rendering account data must never parse markup.' },
    { selector: "MemberExpression[property.name='insertAdjacentHTML']", message: 'Build nodes instead. A page rendering account data must never parse markup.' },
    { selector: "CallExpression[callee.object.name='document'][callee.property.name='write']", message: 'document.write is forbidden.' },
    { selector: "NewExpression[callee.name='Function']", message: 'Runtime code construction defeats the CSP.' },
  ],
  'no-eval': 'error',
  'no-implied-eval': 'error',
}

export default tseslint.config(
  { ignores: ['node_modules/**', 'dist/**', 'test-results/**', 'public/**'] },
  js.configs.recommended,
  {
    rules: {
      'prefer-const': ['error', { ignoreReadBeforeAssign: true }],
      'no-useless-assignment': 'off',
    },
  },
  {
    files: ['src/**/*.ts'],
    extends: [tseslint.configs.recommended],
    languageOptions: {
      parserOptions: { project: ['./tsconfig.json'], tsconfigRootDir: import.meta.dirname },
      globals: globals.browser,
    },
    rules: {
      ...noHtmlSinks,
      'no-console': 'error',
      '@typescript-eslint/no-explicit-any': 'error',
      '@typescript-eslint/no-floating-promises': 'error',
      '@typescript-eslint/no-misused-promises': 'error',
      '@typescript-eslint/await-thenable': 'error',
      '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }],
    },
  },
  {
    files: ['src/**/*.svelte'],
    extends: [tseslint.configs.base, svelte.configs.recommended],
    languageOptions: {
      parser: svelteParser,
      parserOptions: { parser: tseslint.parser, extraFileExtensions: ['.svelte'] },
      globals: globals.browser,
    },
    rules: {
      ...noHtmlSinks,
      'no-console': 'error',
      'no-undef': 'off',
      'no-unused-vars': 'off',
      'no-useless-assignment': 'off',
      '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }],
      'svelte/no-at-html-tags': 'error',
      'svelte/no-target-blank': 'error',
      'svelte/require-each-key': 'warn',
      'svelte/prefer-svelte-reactivity': 'warn',
    },
  },
  {
    files: ['tests/**/*.{ts,mjs}', '*.config.{ts,js}', 'scripts/**/*.mjs'],
    extends: [tseslint.configs.recommended],
    languageOptions: { globals: { ...globals.node, ...globals.browser } },
    rules: {
      'no-console': 'off',
      '@typescript-eslint/no-explicit-any': 'error',
    },
  },
)
