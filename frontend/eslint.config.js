// @ts-check
const eslint = require('@eslint/js');
const { defineConfig } = require('eslint/config');
const tseslint = require('typescript-eslint');
const angular = require('angular-eslint');

module.exports = defineConfig([
  {
    files: ['**/*.ts'],
    extends: [
      eslint.configs.recommended,
      tseslint.configs.recommended,
      tseslint.configs.stylistic,
      angular.configs.tsRecommended,
    ],
    processor: angular.processInlineTemplates,
    rules: {
      // Security rules (CLAUDE.md 15): no eval, no raw HTML, no sanitizer bypass.
      'no-eval': 'error',
      'no-implied-eval': 'error',
      'no-new-func': 'error',
      'no-restricted-properties': [
        'error',
        ...['innerHTML', 'outerHTML'].map((property) => ({
          property,
          message: 'Render dynamic data as text, never as HTML.',
        })),
      ],
      'no-restricted-syntax': [
        'error',
        {
          selector: 'MemberExpression[property.name=/^bypassSecurityTrust/]',
          message: 'Bypassing the Angular sanitizer is forbidden.',
        },
        {
          selector: 'CallExpression[callee.property.name="insertAdjacentHTML"]',
          message: 'Render dynamic data as text, never as HTML.',
        },
      ],
      '@typescript-eslint/no-explicit-any': 'error',
      '@angular-eslint/directive-selector': [
        'error',
        {
          type: 'attribute',
          prefix: 'app',
          style: 'camelCase',
        },
      ],
      '@angular-eslint/component-selector': [
        'error',
        {
          type: 'element',
          prefix: 'app',
          style: 'kebab-case',
        },
      ],
    },
  },
  {
    files: ['**/*.html'],
    extends: [angular.configs.templateRecommended, angular.configs.templateAccessibility],
    rules: {
      // Inline styles break the strict CSP; [innerHTML] renders raw HTML.
      '@angular-eslint/template/no-inline-styles': 'error',
      'no-restricted-syntax': [
        'error',
        {
          selector: 'BoundAttribute[name=/^(innerHTML|outerHTML)$/i]',
          message: 'Render dynamic data as text, never as HTML.',
        },
      ],
    },
  },
]);
