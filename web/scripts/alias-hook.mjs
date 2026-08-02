// Makes Vite's `@/` alias resolvable under plain `node --test`.
//
// src/lib/format.js imports '@/lib/i18n' for the language switch. Vite rewrites
// that at build time (vite.config.js -> resolve.alias), but Node has no such
// mapping, so importing format.js from a test fails with ERR_MODULE_NOT_FOUND.
// That is the whole reason format.js had no tests while stats.js (which has no
// `@/` imports) did — and formatDurationCompact's NaN handling went unchecked.
//
// registerHooks is synchronous and in-process (Node >= 22.15), so this needs no
// worker thread and no extra dependency. Keep the mapping identical to the one
// in vite.config.js.
import { registerHooks } from 'node:module'
import { existsSync } from 'node:fs'
import { fileURLToPath, pathToFileURL } from 'node:url'

const SRC = fileURLToPath(new URL('../src/', import.meta.url))

// Vite resolves extensionless specifiers ('@/lib/i18n' -> i18n.js); Node does
// not, so the extension has to be probed here or every alias import 404s.
const EXTENSIONS = ['', '.js', '.mjs', '.json', '/index.js']

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier.startsWith('@/')) {
      const base = SRC + specifier.slice(2)
      for (const ext of EXTENSIONS) {
        if (!existsSync(base + ext)) continue
        const url = pathToFileURL(base + ext).href
        // Bundlers import JSON with a bare specifier; Node requires an explicit
        // `with { type: 'json' }`. i18n.js imports the two locale files that
        // way, so supply the attribute here rather than editing app source to
        // suit the test runner.
        return url.endsWith('.json')
          ? { url, shortCircuit: true, importAttributes: { type: 'json' } }
          : { url, shortCircuit: true }
      }
      throw new Error(`alias-hook: cannot resolve ${specifier} (tried ${EXTENSIONS.join(', ')})`)
    }
    return nextResolve(specifier, context)
  },
})
