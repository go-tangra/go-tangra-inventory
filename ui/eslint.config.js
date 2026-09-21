import js from '@eslint/js'
import ts from 'typescript-eslint'
import vue from 'eslint-plugin-vue'
import vueParser from 'vue-eslint-parser'
import globals from 'globals'

export default [
  { ignores: ['dist/**', 'node_modules/**', 'src/api/schema.d.ts', 'playwright-report/**', 'test-results/**'] },
  js.configs.recommended,
  ...ts.configs.recommended,
  ...vue.configs['flat/recommended'],
  { languageOptions: { globals: { ...globals.browser } } },
  { files: ['**/*.vue'], languageOptions: { parser: vueParser, parserOptions: { parser: ts.parser } } },
  {
    rules: {
      'vue/multi-word-component-names': 'off',
      // Vuetify templates are attribute-dense; line layout is left to the author.
      'vue/max-attributes-per-line': 'off',
      'vue/singleline-html-element-content-newline': 'off',
    },
  },
]
