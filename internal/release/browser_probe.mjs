import assert from 'node:assert/strict'
import { pathToFileURL } from 'node:url'

const response = await fetch('https://registry.npmjs.org/ws')
if (!response.ok) throw new Error('npm TLS readiness failed')
await response.arrayBuffer()
const { chromium, webkit } = await import(pathToFileURL(process.env.RUNNER_PLAYWRIGHT_MODULE).href)
for (const engine of [
  { name: 'Chromium', type: chromium, executable: process.env.RUNNER_BROWSER_EXECUTABLE },
  { name: 'WebKit', type: webkit, executable: process.env.RUNNER_WEBKIT_EXECUTABLE },
]) {
  if (!engine.executable) throw new Error(`Browser prerequisite missing: configure the installed ${engine.name} executable`)
  let browser
  try {
    browser = await engine.type.launch({ executablePath: engine.executable, headless: true, args: engine.name === 'Chromium' ? ['--disable-gpu', '--single-process', '--no-zygote'] : [] })
    const context = await browser.newContext({ viewport: { width: 320, height: 568 }, isMobile: true, hasTouch: true })
    const page = await context.newPage()
    await page.setContent('<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><button style="width:120px;height:48px">Touch probe</button><script>window.touchProof=0;document.addEventListener("touchstart",e=>{if(e.isTrusted)window.touchProof++});document.querySelector("button").onclick=()=>document.body.dataset.activated="yes"</script>')
    await page.getByRole('button', { name: 'Touch probe', exact: true }).tap()
    assert.deepEqual(await page.evaluate(() => ({ activated: document.body.dataset.activated, trusted: window.touchProof > 0 })), { activated: 'yes', trusted: true })
  } catch (error) {
    throw new Error(`${engine.name} headless touch prerequisite failed: ${error.message}`)
  } finally { if (browser) await browser.close() }
}
console.log('npm TLS, Chromium and WebKit headless native touch runtimes ready')
