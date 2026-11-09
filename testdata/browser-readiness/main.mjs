import assert from 'node:assert/strict'
import http from 'node:http'
import { pathToFileURL } from 'node:url'
import { PageReadiness, BrowserResourceError } from './browser_render.mjs'
const { chromium } = await import(pathToFileURL(process.env.RUNNER_PLAYWRIGHT_MODULE).href)
const server = http.createServer((req, res) => {
  if (req.url.endsWith('.css')) {
    res.setHeader('Content-Type', 'text/css')
    res.end('body{margin:0}.frame{width:' + (req.url === '/overflow.css' ? 600 : 300) + 'px;height:100px}')
    return
  }
  res.setHeader('Content-Type', 'text/html')
  res.end('<html><head>' + (req.url === '/missing' ? '' : '<link rel="stylesheet" href="' + req.url + '.css">') + '</head><body><div class="frame">fixture</div></body></html>')
})
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
const origin = 'http://127.0.0.1:' + server.address().port
const browser = await chromium.launch({ executablePath: process.env.RUNNER_BROWSER_EXECUTABLE, headless: true, args: ['--disable-gpu', '--single-process', '--no-zygote'] })
try {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 } })
  const loads = new Map()
  await context.route('**/*', route => {
    const path = new URL(route.request().url()).pathname
    const n = (loads.get(path) ?? 0) + 1; loads.set(path, n)
    if (path === '/once.css' && n === 1 || path === '/always.css') return route.abort('connectionreset')
    return route.continue()
  })
  const page = await context.newPage(); page.setDefaultTimeout(1500)
  const receipt = {}, ready = new PageReadiness(receipt); ready.watch(page, origin)
  await ready.goto(page, origin + '/once')
  await ready.bounded(page, '.frame')
  assert.equal(receipt.resourceRetries.length, 1, 'transient stylesheet did not get exactly one navigation retry')
  await assert.rejects(ready.goto(page, origin + '/always'), BrowserResourceError)
  assert.equal(receipt.resourceRetries.length, 2, 'persistent failure exceeded one retry per navigation')
  await assert.rejects(ready.goto(page, origin + '/missing'), BrowserResourceError)
  await ready.goto(page, origin + '/overflow')
  await assert.rejects(ready.bounded(page, '.frame'), error => error instanceof assert.AssertionError && error.message.includes('overflow'))
  console.log('PASS: transient resource recovery, persistent failure bound, missing styles, real overflow')
} finally { await browser.close(); await new Promise(resolve => server.close(resolve)) }
