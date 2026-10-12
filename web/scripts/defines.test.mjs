// Unit tests for the define replacement used by scripts/vite.mjs.
//
// Run with:  npm test   (node --test scripts/defines.test.mjs)

import test from 'node:test'
import assert from 'node:assert/strict'
import { neutralizeDefines, sortDefines, BASE_DEFINES } from './vite.mjs'

const D = sortDefines(BASE_DEFINES)
const run = (code) => neutralizeDefines(code, D)

test('replaces defines used as JavaScript', () => {
  assert.equal(run('if (process.env.NODE_ENV !== "production") warn()').code,
    'if ("production" !== "production") warn()')
  assert.equal(run('var e = process.env').code, 'var e = ({})')
  assert.equal(run('var e = process.env;').code, 'var e = ({});')
  assert.equal(run('const m = import.meta.env.MODE').code, 'const m = "production"')
  assert.equal(run('const d = import.meta.env.DEV').code, 'const d = false')
  assert.equal(run('const p = import.meta.env.PROD').code, 'const p = true')
  assert.equal(run('const b = import.meta.env.BASE_URL').code, 'const b = "/"')
  assert.equal(run('const h = import.meta.hot').code, 'const h = undefined')
})

test('leaves member accesses and longer identifiers alone', () => {
  assert.equal(run('window.process.env').code, 'window.process.env')
  assert.equal(run('getGlobalThis().__VUE_OPTIONS_API__ = true').code,
    'getGlobalThis().__VUE_OPTIONS_API__ = true')
  assert.equal(run('myprocess.env').code, 'myprocess.env')
  assert.equal(run('process.environment').code, 'process.environment')
  assert.equal(run('x.process.env.NODE_ENV').code, 'x.process.env.NODE_ENV')
})

test('uses inert text inside strings, template literals and comments', () => {
  // No quote characters may be introduced into a quoted string.
  assert.equal(run('const s = "process.env.NODE_ENV"').code, 'const s = "production"')
  assert.equal(run("const s = 'process.env'").code, "const s = '{}'")
  assert.equal(run('// import.meta.env.MODE').code, '// production')
  assert.equal(run('/* process.env */').code, '/* {} */')
})

test('a template literal substitution is still JavaScript', () => {
  // `${...}` is code, not text: the value must stay a normal expression.
  assert.equal(run('const t = `a${process.env.NODE_ENV}b`').code, 'const t = `a${"production"}b`')
  assert.equal(run('const t = `${process.env}`').code, 'const t = `${({})}`')
  // Nested strings, objects and templates inside the substitution stay intact.
  assert.equal(
    run('const t = `${a[process.env.NODE_ENV]}-${ok ? \'y\' : `z${process.env.X}`}`').code,
    'const t = `${a["production"]}-${ok ? \'y\' : `z${({}).X}`}`'
  )
})

test('template literal rewrite keeps the same evaluated value', () => {
  const { code } = run('const t = `a${process.env.NODE_ENV}b`')
  // Evaluate just the expression: `eval` in module scope has its own scope.
  // eslint-disable-next-line no-eval
  assert.equal(eval(code.replace('const t = ', '')), 'aproductionb')
})

test('a regex literal containing quotes does not desync the scanner', () => {
  const source = 'const re = /["\']/g;\nif (process.env.NODE_ENV !== "production") warn();\n'
  const { code } = run(source)
  assert.match(code, /\/\["'\]\/g/)
  assert.match(code, /if \("production" !== "production"\) warn\(\)/)
})

test('division is not mistaken for a regular expression', () => {
  const source = 'const half = process.env.NODE_ENV.length / 2;'
  const { code } = run(source)
  assert.equal(code, 'const half = "production".length / 2;')
})

test('replacement is idempotent', () => {
  const source = 'if (process.env.NODE_ENV !== "production" && import.meta.env.DEV) warn()'
  const once = run(source).code
  const twice = neutralizeDefines(once, D).code
  assert.equal(twice, once)
  assert.equal(once, 'if ("production" !== "production" && false) warn()')
})

test('escaped quotes inside strings do not end the string early', () => {
  const source = 'const s = "a\\"process.env.NODE_ENV";const after = process.env.NODE_ENV'
  const { code } = run(source)
  assert.equal(code, 'const s = "a\\"production";const after = "production"')
})

test('modules without defines are returned unchanged', () => {
  const source = 'export function add(a, b) { return a + b } // 加法\n'
  assert.equal(run(source).code, source)
  assert.equal(run(source).textReplacements, 0)
})
