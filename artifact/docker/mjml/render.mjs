// MJML renderer of the artifact message profile (AGENT-1614).
//
// Usage: node render.mjs <entry.mjml> <include-root>
//   <entry.mjml>    resolved MJML file (Go templates and fragments already executed)
//   <include-root>  the only directory mj-include may read (the render working directory)
//
// The HTML goes to stdout. Diagnostics go to stderr as one JSON line:
//   {"errors": ["..."]}   (MJML validation or compilation errors)
// Exit code: 0 rendered, 1 MJML refused the document, 2 bad usage.
//
// Fixed options: strict validation, mj-include enabled and bounded to the
// include root, no minification, no comment kept (output must stay stable and
// readable). No network access is attempted by this script.
import fs from 'node:fs'
import path from 'node:path'
import mjml2html from 'mjml'

const [input, root] = process.argv.slice(2)
if (!input || !root) {
  console.error(JSON.stringify({ errors: ['usage: render.mjs <entry.mjml> <include-root>'] }))
  process.exit(2)
}

const fail = (messages) => {
  console.error(JSON.stringify({ errors: messages.slice(0, 20).map((m) => String(m).slice(0, 500)) }))
  process.exit(1)
}

let result
try {
  result = await mjml2html(fs.readFileSync(input, 'utf8'), {
    filePath: path.resolve(input),
    ignoreIncludes: false,
    includePath: [path.resolve(root)],
    keepComments: false,
    minify: false,
    validationLevel: 'strict',
  })
} catch (err) {
  const list = Array.isArray(err.errors) ? err.errors.map((e) => e.formattedMessage || e.message) : [err.message || err]
  fail(list)
}
const errors = (result.errors || []).map((e) => e.formattedMessage || e.message)
if (errors.length > 0) fail(errors)
process.stdout.write(result.html)
console.error(JSON.stringify({ errors: [] }))
