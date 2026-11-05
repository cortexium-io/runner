import fs from 'node:fs'
import assert from 'node:assert/strict'
import { pathToFileURL } from 'node:url'
import { PageReadiness } from './browser_render.mjs'
import { contractVersion, games, mobileViewports, geometryCases, verifyPlayArea, activate, editAndRestore } from './browser_contract.mjs'
const { chromium, webkit } = await import(pathToFileURL(process.env.RUNNER_PLAYWRIGHT_MODULE).href)
const [origin, output] = process.argv.slice(2)
const expectedSizes = { Sudoku: [], Suguru: [6, 8, 10, 12], Nonogram: [5, 10, 15], Binary: [6, 8, 10], Futoshiki: [4, 5, 7], Shikaku: [5, 7, 10], Hashi: [7, 9, 10, 15], Calcudoku: [4, 5, 6, 9], Kakuro: [6, 10, 16] }
const cases = geometryCases(JSON.parse(fs.readFileSync(output + '/../runtime-http.json')))
const receipt = { contract: contractVersion, origin, mockedResponses: false, headless: true, engines: [], journeys: [], geometry: [], complete: false, limitations: ['Headless touch emulation is not a physical iOS/Android device check.', 'Playwright WebKit exposes native taps but no public native touch-drag API; its complete two-corner Shikaku path is verified.'] }
fs.mkdirSync(output, { recursive: true })
const readiness = new PageReadiness(receipt)
const save = () => fs.writeFileSync(output + '/receipt.json', JSON.stringify(receipt, null, 2))
let browser, lastPage
try {
  for (const engine of [{ name: 'chromium', type: chromium, executable: process.env.RUNNER_BROWSER_EXECUTABLE }, { name: 'webkit', type: webkit, executable: process.env.RUNNER_WEBKIT_EXECUTABLE }]) {
    if (!engine.executable) throw new Error(`Browser prerequisite missing: configured ${engine.name} executable`)
    browser = await engine.type.launch({ executablePath: engine.executable, headless: true, args: engine.name === 'chromium' ? ['--disable-gpu', '--single-process', '--no-zygote'] : [] })
    receipt.engines.push(engine.name)
    for (const device of [{ name: 'desktop', width: 1440, height: 1000, touch: false }, { name: 'mobile', ...mobileViewports[0], touch: true }]) {
      const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: device.width, height: device.height }, isMobile: device.touch, hasTouch: device.touch })
      await context.addInitScript(() => {
        window.runnerInputEvidence = { touch: 0, keyboard: 0 }
        document.addEventListener('pointerdown', e => { if (e.isTrusted && e.pointerType === 'touch') window.runnerInputEvidence.touch++ }, true)
        document.addEventListener('touchstart', e => { if (e.isTrusted) window.runnerInputEvidence.touch++ }, true)
        document.addEventListener('keydown', e => { if (e.isTrusted) window.runnerInputEvidence.keyboard++ }, true)
      })
      await context.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort())
      const page = await context.newPage(); lastPage = page
      page.setDefaultTimeout(20000)
      readiness.watch(page, origin)
      for (const game of games) {
        const record = { engine: engine.name, device: device.name, game, states: [] }
        receipt.journeys.push(record)
        await readiness.goto(page, origin)
        await activate(page, page.getByRole('link', { name: 'Choose ' + game, exact: true }), device.touch, false)
        const dialog = page.getByRole('dialog', { name: 'New ' + game, exact: true })
        await dialog.waitFor({ state: 'visible' })
        await readiness.bounded(page, '.setup-dialog')
        await page.waitForFunction(() => !document.body.innerText.includes('Checking puzzle settings…'))
        const more = dialog.locator('details.setup-more')
        assert.equal(await more.getAttribute('open'), null, 'default More options should be closed: ' + game)
        const play = dialog.getByRole('button', { name: 'Play ' + game, exact: true })
        if (game === 'Sudoku') assert.equal(await dialog.getByRole('group', { name: 'Board size', exact: true }).count(), 0)
        else {
          const sizes = dialog.locator('fieldset').filter({ has: page.locator('legend').filter({ hasText: /^(Board size|Total board size, including blocks)$/ }) })
          await sizes.getByRole('button').nth(expectedSizes[game].length - 1).waitFor({ state: 'visible' })
          const labels = await sizes.getByRole('button').allTextContents()
          assert.deepEqual(labels.map(t => Number(t.trim().split(' ')[0])), expectedSizes[game], 'main sizes: ' + game)
          for (const option of await sizes.getByRole('button').all()) {
            await activate(page, option, device.touch)
            assert.ok(await play.isEnabled(), 'main size preset requires repairing a hidden setting: ' + game + ' ' + await option.textContent())
          }
          await activate(page, sizes.getByRole('button').first(), device.touch)
        }
        await activate(page, more.locator('summary'), device.touch)
        assert.equal(await more.getAttribute('open'), '', 'More options did not expand')
        if (game === 'Suguru') {
          assert.ok(await more.getByRole('button', { name: '10 × 15', exact: true }).isVisible())
          assert.ok(await more.getByRole('button', { name: '6', exact: true }).isVisible())
        }
        await activate(page, more.locator('summary'), device.touch)
        record.states.push('valid primary presets and secondary options')
        assert.ok(await play.isEnabled(), 'default unavailable: ' + game)
        const response = page.waitForResponse(r => r.url() === origin + '/api/games/next')
        await activate(page, play, device.touch)
        assert.equal((await response).status(), 200, 'New: ' + game)
        await page.waitForURL(url => url.pathname.startsWith('/' + game.toLowerCase() + '/'))
        record.path = new URL(page.url()).pathname
        await page.locator('.player-board-frame:visible').first().waitFor({ state: 'visible' })
        await readiness.ready(page)
        // This runs before any locator interaction can scroll the board into view.
        record.bounds = await verifyPlayArea(page, device.touch)
        record.states.push('New enters usable initial play viewport')
        record.edit = await editAndRestore(page, game, device.touch, engine.name, readiness)
        record.states.push('actual edit, Undo, Redo, saved visible state survives reload')
        // A separate fresh profile keeps the deliberate arbitrary edit from
        // making an otherwise valid hint correctly request a recheck first.
        const supportContext = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: device.width, height: device.height }, isMobile: device.touch, hasTouch: device.touch })
        await supportContext.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort())
        const support = await supportContext.newPage(); lastPage = support; support.setDefaultTimeout(20000); readiness.watch(support, origin)
        await readiness.goto(support, origin + record.path)
        await support.locator('.player-board-frame:visible').first().waitFor({ state: 'visible' })
        const hint = support.getByRole('button', { name: 'A little hint', exact: true })
        if (await hint.count()) {
          assert.ok(await hint.isEnabled(), 'hint disabled: ' + game)
          const response = support.waitForResponse(r => new URL(r.url()).pathname === '/api/games/hint' || r.request().method() === 'POST' && r.request().postDataJSON()?.kind === 'hint')
          await activate(support, hint, device.touch, false)
          assert.equal((await response).status(), 200, 'hint HTTP: ' + game)
          await support.getByRole('heading', { name: 'Why this move?', exact: true }).waitFor({ state: 'visible' })
          record.states.push('checked hint reason')
        }
        await activate(support, support.getByRole('button', { name: 'Print board', exact: true }), device.touch, false)
        await support.locator('iframe[title="Printable board preview"]').waitFor({ state: 'visible' })
        await support.frameLocator('iframe[title="Printable board preview"]').locator('table, svg').first().waitFor({ state: 'visible' })
        await activate(support, support.getByRole('button', { name: 'Close preview', exact: true }), device.touch)
        record.states.push('print preview')
        readiness.assertNoErrors(support)
        await supportContext.close(); lastPage = page
        await page.screenshot({ path: `${output}/${engine.name}-${device.name}-${game.toLowerCase()}.png`, fullPage: true })
        save()
      }
      readiness.assertNoErrors(page)
      await context.close()
    }
    // All advertised board dimensions are checked without regenerating every
    // difficulty: identities come from the already-passed native catalogue proof.
    for (const viewport of mobileViewports) {
      const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport, isMobile: true, hasTouch: true })
      await context.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort())
      const page = await context.newPage(); lastPage = page; page.setDefaultTimeout(20000); readiness.watch(page, origin)
      for (const sample of cases) {
        await readiness.goto(page, origin + sample.path)
        await page.locator('.player-board-frame:visible').first().waitFor({ state: 'visible' })
        const metrics = await verifyPlayArea(page, true)
        receipt.geometry.push({ engine: engine.name, width: viewport.width, key: sample.key, path: sample.path, board: metrics.board, verifiedZoom: metrics.verifiedZoom === true })
      }
      readiness.assertNoErrors(page)
      await context.close(); save()
    }
    await browser.close(); browser = undefined
  }
  receipt.complete = true
} catch (error) {
  receipt.failure = error.stack
  if (lastPage && !lastPage.isClosed()) {
    await lastPage.screenshot({ path: output + '/failure.png', fullPage: false })
    receipt.failureText = (await lastPage.locator('body').innerText()).slice(-8000)
  }
  throw error
} finally {
  if (browser) await browser.close()
  save()
}
console.log(JSON.stringify({ complete: true, contract: contractVersion, journeys: receipt.journeys.length, geometries: receipt.geometry.length }))
