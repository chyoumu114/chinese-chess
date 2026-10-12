// Vite entry point for `dev`, `build` and `preview`.
//
// WHY THIS EXISTS
// ---------------
// Vite 5 loads vite.config.js by bundling it with esbuild, and esbuild runs as a
// child process that speaks a protocol over stdio pipes. Some sandboxes (the DSH
// file sandbox, for instance) forbid piped stdio for child processes, so esbuild
// cannot start at all: `vite`, `vite build` and `vite preview` all die with
// `spawn EPERM` before the config is even read.
//
// This wrapper probes esbuild once and then picks a path:
//
//   esbuild usable   -> the completely standard route: Vite's own loader reads
//                       vite.config.js, esbuild transforms/minifies as usual.
//                       This is what runs on a normal machine.
//
//   esbuild blocked  -> vite.config.js is imported natively (it is an ESM file)
//                       and handed to Vite with `configFile: false`. The
//                       esbuild-backed pieces are replaced in-process:
//                         * `esbuild: false` removes the esbuild transform plugin
//                         * minification and CSS minification are turned off
//                         * Vite's own define pass is dropped (it is esbuild-based)
//                           and its replacements are performed by
//                           `neutralizeDefinesPlugin`
//                         * dependency pre-bundling is skipped in dev
//                       Rollup, @vue/compiler-sfc and the Vue SFC compiler then
//                       do all the work in-process. The only visible difference
//                       is that build output is not minified.
//
// Both paths are driven by the same vite.config.js, so `base`, `outDir`, the Vue
// plugin and the dev proxy stay in exactly one place.

import path from 'node:path'
import { readFileSync } from 'node:fs'
import { fileURLToPath, pathToFileURL } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const configPath = path.join(root, 'vite.config.js')

// ---------------------------------------------------------------------------
// define replacement
// ---------------------------------------------------------------------------

/**
 * Build-mode defines, as `[key, javascriptValue, plainTextValue]`.
 *
 * `javascriptValue` is used where the token is real JavaScript; `plainTextValue`
 * is used inside strings, template literals and comments, where a quoted/braced
 * replacement would corrupt the surrounding text. Both stay syntactically valid.
 *
 * Longer keys must win over shorter ones (`process.env.NODE_ENV` before
 * `process.env`), which `sortDefines` guarantees.
 */
const BASE_DEFINES = [
  ['process.env.NODE_ENV', '"production"', 'production'],
  ['global.process.env.NODE_ENV', '"production"', 'production'],
  ['globalThis.process.env.NODE_ENV', '"production"', 'production'],
  ['process.env', '({})', '{}'],
  ['global.process.env', '({})', '{}'],
  ['globalThis.process.env', '({})', '{}'],
  ['import.meta.hot', 'undefined', 'undefined'],
  ['import.meta.env.SSR', 'false', 'false'],
  ['import.meta.env.MODE', '"production"', 'production'],
  ['import.meta.env.PROD', 'true', 'true'],
  ['import.meta.env.DEV', 'false', 'false'],
  ['import.meta.env.BASE_URL', '"/"', '/'],
  ['import.meta.env', 'undefined', 'undefined']
]

const WORD = /[A-Za-z0-9_$]/
const KEYWORDS_BEFORE_REGEX = new Set([
  'return', 'typeof', 'case', 'in', 'of', 'new', 'delete', 'void',
  'instanceof', 'do', 'else', 'yield', 'await', 'throw'
])

function escapeRegex(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

/** toCodeValue renders a Vite define value as JavaScript. */
function toCodeValue(value) {
  return typeof value === 'string' ? value : JSON.stringify(value)
}

/** toTextValue renders a Vite define value as inert text. */
function toTextValue(value) {
  if (typeof value === 'string') {
    if (value.length >= 2 && value.startsWith('"') && value.endsWith('"')) {
      try {
        return String(JSON.parse(value))
      } catch {
        /* not a JSON string literal: keep it verbatim */
      }
    }
    return value
  }
  return String(value)
}

/** sortDefines orders defines so that the longest key is tried first. */
function sortDefines(defines) {
  return [...defines].sort((a, b) => b[0].length - a[0].length)
}

/**
 * resolveDefines mirrors the defines Vite would apply for this configuration:
 * the user/plugin `define` entries plus Vite's own ones. `import.meta.env.*` is
 * only replaced for a build, exactly as Vite does (in dev those are served by
 * the import-analysis and client-injection plugins instead).
 */
function resolveDefines(config) {
  const isBuild = config.command === 'build'
  const isBuildLib = isBuild && !!config.build?.lib
  const list = Object.entries(config.define || {}).map(([key, value]) => [
    key,
    toCodeValue(value),
    toTextValue(value)
  ])

  if (!isBuildLib) {
    const nodeEnv = JSON.stringify(process.env.NODE_ENV || config.mode)
    const nodeEnvText = toTextValue(nodeEnv)
    list.push(
      ['process.env', '({})', '{}'],
      ['global.process.env', '({})', '{}'],
      ['globalThis.process.env', '({})', '{}'],
      ['process.env.NODE_ENV', nodeEnv, nodeEnvText],
      ['global.process.env.NODE_ENV', nodeEnv, nodeEnvText],
      ['globalThis.process.env.NODE_ENV', nodeEnv, nodeEnvText]
    )
  }

  if (isBuild) {
    const mode = JSON.stringify(config.mode)
    list.push(
      ['import.meta.hot', 'undefined', 'undefined'],
      ['import.meta.env.SSR', 'false', 'false'],
      ['import.meta.env.MODE', mode, config.mode],
      ['import.meta.env.PROD', String(config.isProduction), String(config.isProduction)],
      ['import.meta.env.DEV', String(!config.isProduction), String(!config.isProduction)],
      ['import.meta.env.BASE_URL', JSON.stringify(config.base), config.base],
      ['import.meta.env', 'undefined', 'undefined']
    )
  }

  return sortDefines(list)
}

/** matches a define at `i` as JavaScript, or returns null. */
function matchDefine(code, i, defines) {
  const before = i === 0 ? '' : code[i - 1]
  // Never rewrite a member access (`window.process.env`, `x.__VUE_OPTIONS_API__`)
  // nor a longer identifier.
  if (before && (WORD.test(before) || before === '.')) return null
  for (const [key, value] of defines) {
    if (!code.startsWith(key, i)) continue
    const after = code[i + key.length] || ''
    if (after && WORD.test(after)) continue
    return { length: key.length, value }
  }
  return null
}

/** matches a define at `i` anywhere, for text contexts. */
function matchDefineLoose(code, i, defines) {
  for (const [key, , text] of defines) {
    if (!code.startsWith(key, i)) continue
    const after = code[i + key.length] || ''
    if (after && WORD.test(after)) continue
    return { length: key.length, value: text }
  }
  return null
}

/** true when a `/` at this point starts a regular expression, not a division. */
function regexAllowed(prevSig, word) {
  if (!prevSig) return true
  if ('(,=:[!&|?{};+-*%~^<>'.includes(prevSig)) return true
  return KEYWORDS_BEFORE_REGEX.has(word)
}

/**
 * neutralizeDefines applies the given defines to a module's source.
 * @returns {{ code: string, textReplacements: number }}
 */
function neutralizeDefines(code, defines = BASE_DEFINES) {
  const out = []
  const n = code.length
  // Context stack. 'code' frames hold JavaScript and 'template' frames hold the
  // literal text of a template literal, while `${` pushes a 'tpl-expr' frame
  // whose braces are counted so that the matching `}` returns to template text.
  const stack = [{ kind: 'code' }]
  let i = 0
  let prevSig = ''
  let word = ''
  let textReplacements = 0

  const top = () => stack[stack.length - 1]

  while (i < n) {
    const frame = top()
    const ch = code[i]
    const next = i + 1 < n ? code[i + 1] : ''

    if (frame.kind === 'code' || frame.kind === 'tpl-expr') {
      if (ch === '/' && next === '/') {
        stack.push({ kind: 'line-comment' })
        out.push('//')
        i += 2
        continue
      }
      if (ch === '/' && next === '*') {
        stack.push({ kind: 'block-comment' })
        out.push('/*')
        i += 2
        continue
      }
      if (ch === '/' && regexAllowed(prevSig, word)) {
        stack.push({ kind: 'regex', inClass: false })
        out.push('/')
        i += 1
        continue
      }
      if (ch === "'" || ch === '"') {
        stack.push({ kind: 'string', quote: ch })
        out.push(ch)
        i += 1
        continue
      }
      if (ch === '`') {
        stack.push({ kind: 'template' })
        out.push(ch)
        i += 1
        continue
      }
      if (frame.kind === 'tpl-expr') {
        if (ch === '{') {
          frame.braces += 1
          out.push(ch)
          i += 1
          prevSig = '{'
          word = ''
          continue
        }
        if (ch === '}') {
          out.push(ch)
          i += 1
          if (frame.braces === 0) {
            stack.pop() // back to the enclosing template's literal text
            prevSig = ')'
          } else {
            frame.braces -= 1
            prevSig = '}'
          }
          word = ''
          continue
        }
      }
      const hit = matchDefine(code, i, defines)
      if (hit) {
        out.push(hit.value)
        i += hit.length
        prevSig = '('
        word = ''
        continue
      }
      out.push(ch)
      if (!/\s/.test(ch)) prevSig = ch
      word = WORD.test(ch) ? word + ch : ''
      i += 1
      continue
    }

    if (frame.kind === 'line-comment') {
      if (ch === '\n') {
        stack.pop()
        prevSig = ''
        out.push(ch)
        i += 1
        continue
      }
      const hit = matchDefineLoose(code, i, defines)
      if (hit) {
        out.push(hit.value)
        i += hit.length
        textReplacements += 1
        continue
      }
      out.push(ch)
      i += 1
      continue
    }

    if (frame.kind === 'block-comment') {
      if (ch === '*' && next === '/') {
        stack.pop()
        out.push('*/')
        i += 2
        continue
      }
      const hit = matchDefineLoose(code, i, defines)
      if (hit) {
        out.push(hit.value)
        i += hit.length
        textReplacements += 1
        continue
      }
      out.push(ch)
      i += 1
      continue
    }

    if (frame.kind === 'regex') {
      if (ch === '\\') {
        out.push(ch + next)
        i += 2
        continue
      }
      if (ch === '[') frame.inClass = true
      else if (ch === ']') frame.inClass = false
      if (ch === '/' && !frame.inClass) {
        let j = i + 1
        while (j < n && /[a-z]/i.test(code[j])) j += 1
        out.push(code.slice(i, j))
        i = j
        stack.pop()
        prevSig = '('
        word = ''
        continue
      }
      out.push(ch)
      i += 1
      continue
    }

    if (frame.kind === 'template') {
      if (ch === '\\') {
        out.push(ch + next)
        i += 2
        continue
      }
      if (ch === '`') {
        stack.pop()
        out.push(ch)
        i += 1
        prevSig = '('
        word = ''
        continue
      }
      if (ch === '$' && next === '{') {
        stack.push({ kind: 'tpl-expr', braces: 0 })
        out.push('${')
        i += 2
        continue
      }
      const hit = matchDefineLoose(code, i, defines)
      if (hit) {
        out.push(hit.value)
        i += hit.length
        textReplacements += 1
        continue
      }
      out.push(ch)
      i += 1
      continue
    }

    // frame.kind === 'string'
    if (ch === '\\') {
      out.push(ch + next)
      i += 2
      continue
    }
    if (ch === frame.quote) {
      stack.pop()
      out.push(ch)
      i += 1
      prevSig = '('
      word = ''
      continue
    }
    const hit = matchDefineLoose(code, i, defines)
    if (hit) {
      out.push(hit.value)
      i += hit.length
      textReplacements += 1
      continue
    }
    out.push(ch)
    i += 1
  }

  return { code: out.join(''), textReplacements }
}

/**
 * neutralizeDefinesPlugin performs the replacements Vite's own define pass would
 * have made, without esbuild. It rewrites in `load`, which runs before every
 * transform hook, so no plugin can observe the unreplaced tokens.
 */
function neutralizeDefinesPlugin() {
  const debug = !!process.env.XIANGQI_DEBUG
  let defines = sortDefines(BASE_DEFINES)
  let pattern = new RegExp(defines.map(([key]) => escapeRegex(key)).join('|'))
  let textHits = 0
  let hits = 0

  function rewrite(id, code) {
    if (!pattern.test(code)) return null
    const { code: next, textReplacements } = neutralizeDefines(code, defines)
    if (next === code) {
      // Only unreplaceable occurrences, e.g. the property access
      // `getGlobalThis().__VUE_OPTIONS_API__`, which esbuild leaves alone too.
      return null
    }
    // A second pass must be a no-op: that proves no *replaceable* occurrence is
    // left, which is what Vite's define pass reacts to.
    if (neutralizeDefines(next, defines).code !== next) {
      throw new Error(`define 替换不完整（${id}）：Vite 的 define 阶段会因此调用不可用的 esbuild`)
    }
    hits += 1
    textHits += textReplacements
    if (debug) console.warn(`[vite] neutralised ${id} (textReplacements=${textReplacements})`)
    return { code: next, map: null }
  }

  return {
    name: 'xiangqi:neutralize-defines',
    enforce: 'pre',
    configResolved(config) {
      // Includes the Vue feature flags contributed by @vitejs/plugin-vue.
      defines = resolveDefines(config)
      pattern = new RegExp(defines.map(([key]) => escapeRegex(key)).join('|'))
      if (debug) {
        console.warn(`[vite] defines = ${defines.map(([key]) => key).join(', ')}`)
      }
    },
    load(id) {
      if (!isPlainJsFile(id)) return null
      let code
      try {
        code = readFileSync(id, 'utf8')
      } catch {
        return null
      }
      return rewrite(id, code)
    },
    transform(code, id) {
      // Backstop for code produced by earlier transforms.
      if (id.includes('\0')) return null
      return rewrite(id, code)
    },
    buildEnd() {
      if (debug) console.warn(`[vite] neutralised ${hits} module(s)`)
      if (textHits > 0) {
        console.warn(
          `[vite] 有 ${textHits} 处 define 出现在字符串或注释中，已替换为等价文本（至多影响开发期提示文本）。`
        )
      }
    }
  }
}

/** isPlainJsFile reports whether an id is a real .js/.mjs/.cjs file on disk. */
function isPlainJsFile(id) {
  if (!id || id.includes('\0') || id.includes('?')) return false
  if (!/\.(m?js|cjs)$/.test(id)) return false
  return path.isAbsolute(id)
}

/**
 * dropDefinePlugin removes Vite's own define pass from the plugin list.
 *
 * That pass calls `esbuild.transform` for every module that mentions a define,
 * and Vite's patched commonjs resolver can invoke it on a file read straight
 * from disk, which no `load` hook can intercept. `neutralizeDefinesPlugin`
 * performs the same replacements, so nothing is lost.
 */
function dropDefinePlugin() {
  return {
    name: 'xiangqi:drop-vite-define',
    enforce: 'pre',
    options(rawOptions) {
      const flat = (rawOptions.plugins || []).flat(Infinity)
      return { ...rawOptions, plugins: flat.filter((p) => !p || p.name !== 'vite:define') }
    }
  }
}

// ---------------------------------------------------------------------------
// entry point
// ---------------------------------------------------------------------------

/** probeEsbuild reports whether esbuild's helper process can be started. */
async function probeEsbuild() {
  try {
    const esbuild = await import('esbuild')
    // A tiny transform is enough to force the service process to start.
    esbuild.transformSync('export const probe = 1', { loader: 'js' })
    return true
  } catch (err) {
    const reason = err && (err.code || err.message)
    console.warn(
      `[vite] esbuild 不可用（${reason}），改用进程内模式：Rollup + @vue/compiler-sfc，构建不做压缩。`
    )
    return false
  }
}

/** loadUserConfig imports vite.config.js natively, bypassing Vite's esbuild loader. */
async function loadUserConfig() {
  // Windows absolute paths must be file:// URLs for the ESM loader.
  const mod = await import(pathToFileURL(configPath).href)
  return (mod && mod.default) || mod || {}
}

/** fallbackConfig builds the in-process configuration for a command. */
function fallbackConfig(userConfig, command) {
  const config = {
    ...userConfig,
    root,
    configFile: false,
    esbuild: false,
    plugins: [dropDefinePlugin(), ...(userConfig.plugins || []), neutralizeDefinesPlugin()],
    // Vite probes for mapped network drives with `exec("net use")`, which is
    // another piped child process the sandbox refuses. Skipping realpath
    // canonicalisation avoids that probe; with an npm (non symlinked) tree the
    // resolved paths are identical.
    resolve: { ...(userConfig.resolve || {}), preserveSymlinks: true },
    build: {
      ...(userConfig.build || {}),
      minify: false,
      cssMinify: false
    }
  }

  if (command === 'dev') {
    // Pre-bundling is esbuild-based, so dependencies are served unbundled.
    config.optimizeDeps = {
      ...(userConfig.optimizeDeps || {}),
      noDiscovery: true,
      include: []
    }
  }

  return config
}

async function runBuild() {
  const { build } = await import('vite')
  if (await probeEsbuild()) {
    await build({ root, configFile: configPath })
    return
  }
  await build(fallbackConfig(await loadUserConfig(), 'build'))
}

async function runDev() {
  const { createServer } = await import('vite')
  const usable = await probeEsbuild()
  const server = await createServer(
    usable ? { root, configFile: configPath } : fallbackConfig(await loadUserConfig(), 'dev')
  )
  await server.listen()
  server.printUrls()
  console.log('[vite] 开发服务器已就绪（Ctrl+C 退出）')
}

async function runPreview() {
  const { preview } = await import('vite')
  const usable = await probeEsbuild()
  const server = await preview(
    usable ? { root, configFile: configPath } : fallbackConfig(await loadUserConfig(), 'preview')
  )
  server.printUrls()
}

const COMMANDS = { build: runBuild, dev: runDev, preview: runPreview }

async function main() {
  const command = (process.argv[2] || 'build').trim()
  const run = COMMANDS[command]
  if (!run) {
    console.error(`未知命令：${command}（可用：${Object.keys(COMMANDS).join(', ')}）`)
    process.exit(2)
  }
  await run()
}

const isMain = process.argv[1] && pathToFileURL(process.argv[1]).href === import.meta.url

if (isMain) {
  main().catch((err) => {
    console.error(err)
    process.exit(1)
  })
}

// Exported for the tests in scripts/defines.test.mjs.
export { neutralizeDefines, resolveDefines, sortDefines, BASE_DEFINES, neutralizeDefinesPlugin }
