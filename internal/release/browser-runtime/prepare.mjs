// Operator-owned bootstrap only. Runtime images are never built by product tasks.
import fs from 'node:fs'
import path from 'node:path'
import crypto from 'node:crypto'
import { fileURLToPath } from 'node:url'
const [output] = process.argv.slice(2)
if (!output || !path.isAbsolute(output)) throw new Error('absolute new output directory required')
fs.mkdirSync(output, { mode: 0o700 })
const own = path.dirname(fileURLToPath(import.meta.url))
const names = ['browser.mjs', 'browser_render.mjs', 'browser_contract.mjs', 'browser_container.mjs', 'browser_fixture.mjs']
const hash = data => crypto.createHash('sha256').update(data).digest('hex')
let manifest = ''
for (const name of names) {
  const data = fs.readFileSync(path.join(own, '..', name))
  fs.writeFileSync(path.join(output, name), data, { mode: 0o600 }); manifest += `${name}:${hash(data)}\n`
}
for (const name of ['Dockerfile', 'package.json', 'package-lock.json']) fs.copyFileSync(path.join(own, name), path.join(output, name), fs.constants.COPYFILE_EXCL)
console.log(JSON.stringify({ driver: hash(manifest), context: output, base: 'sha256:ee15712262f017b8157421b212b12099146645f44b1bc5c0514cbbe6a229b1d0' }))
