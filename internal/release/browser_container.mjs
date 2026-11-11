// Fixed trusted container entrypoint. Task data cannot select code or commands.
import fs from 'node:fs'
import crypto from 'node:crypto'
import https from 'node:https'
import http from 'node:http'
import { spawn } from 'node:child_process'
import { once } from 'node:events'
import { createRequire } from 'node:module'

const output = '/work/browser'
fs.mkdirSync(output, { mode: 0o700 })
let proxy, input
const driverFiles = ['browser.mjs', 'browser_render.mjs', 'browser_contract.mjs', 'browser_container.mjs', 'browser_fixture.mjs']
const hash = data => crypto.createHash('sha256').update(data).digest('hex')
const driver = hash(driverFiles.map(name => `${name}:${hash(fs.readFileSync('/runner/' + name))}\n`).join(''))
const require = createRequire(import.meta.url)
const version = require('playwright/package.json').version
const browserVersions = JSON.parse(fs.readFileSync('/runner/node_modules/playwright-core/browsers.json')).browsers.filter(row => ['chromium-headless-shell', 'webkit'].includes(row.name)).map(({ name, revision, browserVersion }) => ({ name, revision, browserVersion }))
const runtimeIdentity = () => ({ image: input?.image, driver, playwright: version, node: process.versions.node, engines: browserVersions })

function archive() {
  const parts = []; let total = 0
  for (const name of fs.readdirSync(output).sort()) {
    if (!/^(receipt\.json|admission-backoffs\.json|failure\.png|(chromium|webkit)-(desktop|mobile)-(sudoku|suguru|nonogram|binary|futoshiki|shikaku|hashi|calcudoku|kakuro)\.png)$/.test(name)) throw new Error('unexpected browser evidence filename')
    const stat = fs.lstatSync(output + '/' + name)
    if (!stat.isFile() || stat.size > (name === 'receipt.json' ? 128 * 1024 : name === 'admission-backoffs.json' ? 1024 * 1024 : 8 * 1024 * 1024)) throw new Error('browser evidence is not a bounded regular file')
    const body = fs.readFileSync(output + '/' + name)
    total += body.length
    if (total > 64 * 1024 * 1024) throw new Error('browser evidence exceeds archive bound')
    const header = Buffer.alloc(512)
    header.write(name)
    const oct = (value, offset, length) => header.write(value.toString(8).padStart(length - 1, '0') + '\0', offset, length)
    oct(0o600, 100, 8); oct(1000, 108, 8); oct(1000, 116, 8); oct(body.length, 124, 12); oct(0, 136, 12)
    header.fill(32, 148, 156); header.write('0', 156); header.write('ustar\0', 257); header.write('00', 263)
    header.write([...header].reduce((a, b) => a + b, 0).toString(8).padStart(6, '0') + '\0 ', 148, 8)
    parts.push(header, body, Buffer.alloc((512 - body.length % 512) % 512))
  }
  parts.push(Buffer.alloc(1024))
  return Buffer.concat(parts)
}

try {
  let raw = Buffer.alloc(0)
  for await (const chunk of process.stdin) {
    if (raw.length + chunk.length > 5 * 1024 * 1024) throw new Error('browser input exceeds bound')
    raw = Buffer.concat([raw, chunk])
  }
  input = JSON.parse(raw)
  if (!['readiness', 'qualification'].includes(input.mode) || !/^sha256:[0-9a-f]{64}$/.test(input.image) || !Number.isInteger(input.timeoutMs) || input.timeoutMs < 1 || input.timeoutMs > 20 * 60 * 1000) throw new Error('invalid fixed browser invocation')
  if (driver !== process.env.RUNNER_BROWSER_DRIVER_SHA256 || version !== '1.64.1') throw new Error('installed browser runtime/driver identity differs')
  const { webkit } = await import('/runner/node_modules/playwright/index.mjs')
  const env = { PATH: '/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin', HOME: '/tmp/browser-home', TMPDIR: '/tmp', LANG: 'C.UTF-8', PLAYWRIGHT_BROWSERS_PATH: '/ms-playwright', RUNNER_PLAYWRIGHT_MODULE: '/runner/node_modules/playwright/index.mjs', RUNNER_BROWSER_EXECUTABLE: '/ms-playwright/chromium_headless_shell-1248/chrome-headless-shell-linux64/chrome-headless-shell', RUNNER_WEBKIT_EXECUTABLE: webkit.executablePath() }
  fs.mkdirSync(env.HOME, { mode: 0o700 })
  let args = ['/runner/browser_fixture.mjs', output]
  if (input.mode === 'qualification') {
    if (typeof input.catalogue !== 'object' || typeof input.cert !== 'string' || typeof input.key !== 'string' || input.cert.length > 10000 || input.key.length > 10000) throw new Error('invalid browser qualification input')
    fs.writeFileSync('/work/runtime-http.json', JSON.stringify(input.catalogue), { mode: 0o600 })
    proxy = https.createServer({ cert: input.cert, key: input.key }, (req, res) => {
      const { 'x-runner-browser-client': rawProfile, ...headers } = req.headers
      const profile = typeof rawProfile === 'string' && /^[1-9]\d?$/.test(rawProfile) && Number(rawProfile) <= 64 ? rawProfile : '1'
      const upstream = http.request({ host: '127.0.0.1', port: 8080, method: req.method, path: req.url, headers: { ...headers, host: '127.0.0.1:9443', 'x-forwarded-proto': 'https', 'x-forwarded-for': '198.18.0.' + profile } }, response => { res.writeHead(response.statusCode, response.headers); response.pipe(res) })
      upstream.on('error', () => { res.writeHead(502); res.end('Disposable candidate unavailable') })
      req.pipe(upstream)
    })
    proxy.listen(9443, '127.0.0.1'); await once(proxy, 'listening')
    args = ['/runner/browser.mjs', 'https://127.0.0.1:9443', output]
  }
  let tail = ''
  env.RUNNER_BROWSER_DEADLINE_MS = String(Date.now() + input.timeoutMs)
  const child = spawn(process.execPath, args, { env, stdio: ['ignore', 'pipe', 'pipe'], timeout: input.timeoutMs, killSignal: 'SIGKILL' })
  for (const stream of [child.stdout, child.stderr]) stream.on('data', chunk => { tail = (tail + chunk).slice(-12000) })
  const [code, signal] = await once(child, 'close')
  let receipt = {}
  if (fs.existsSync(output + '/receipt.json')) receipt = JSON.parse(fs.readFileSync(output + '/receipt.json'))
  if (input.mode === 'readiness') receipt = { contract: 2, mode: 'readiness', complete: code === 0, engines: code === 0 ? ['chromium', 'webkit'] : [], admissionBackoffs: receipt.admissionBackoffs }
  if (code !== 0 && !receipt.failure) receipt.failure = `BrowserRuntimeError: trusted driver failed (${code ?? signal}): ${tail}`
  receipt.runtime = runtimeIdentity()
  receipt.transportComplete = true
  fs.writeFileSync(output + '/receipt.json', JSON.stringify(receipt), { mode: 0o600 })
} catch (error) {
  fs.writeFileSync(output + '/receipt.json', JSON.stringify({ contract: 2, complete: false, transportComplete: true, failure: 'BrowserRuntimeError: ' + error.message, runtime: runtimeIdentity() }), { mode: 0o600 })
} finally {
  if (proxy) await new Promise(resolve => proxy.close(resolve))
}
// A zero container exit means complete evidence transport, never acceptance.
// The owning Go adapter separately checks failures and every receipt obligation.
if (!process.stdout.write(archive())) await once(process.stdout, 'drain')
