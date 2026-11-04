import fs from 'node:fs'
import assert from 'node:assert/strict'
import { pathToFileURL } from 'node:url'
import { PageReadiness } from './browser_render.mjs'
const { chromium } = await import(pathToFileURL(process.env.RUNNER_PLAYWRIGHT_MODULE).href)

const [origin, output, resume] = process.argv.slice(2)
const games = ['Sudoku', 'Suguru', 'Nonogram', 'Binary', 'Futoshiki', 'Shikaku', 'Hashi', 'Calcudoku', 'Kakuro']
const expectedSizes = { Sudoku: [], Suguru: [6, 8, 10, 12], Nonogram: [5, 10, 15], Binary: [6, 8, 10], Futoshiki: [4, 5, 7], Shikaku: [5, 7, 10], Hashi: [7, 9, 10, 15], Calcudoku: [4, 5, 6, 9], Kakuro: [6, 10, 16] }
const receipt = resume ? { ...JSON.parse(fs.readFileSync(resume + '/receipt.json')), complete: false, failure: undefined, reusedEvidence: resume } : { origin, mockedResponses: false, headless: true, journeys: [], complete: false }
const paths = Object.fromEntries(receipt.journeys.filter(row => row.path).map(row => [row.game, row.path]))
fs.mkdirSync(output, { recursive: true })
const readiness = new PageReadiness(receipt)
const bounded = (page, selector) => readiness.bounded(page, selector)
let browser
let lastPage
try {
  for (const device of [{ name: 'desktop', width: 1440, height: 1000, touch: false }, { name: 'mobile', width: 390, height: 844, touch: true }]) {
    browser = await chromium.launch({ executablePath: process.env.RUNNER_BROWSER_EXECUTABLE, headless: true, args: ['--disable-gpu', '--single-process', '--no-zygote'] })
    const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: device.width, height: device.height }, isMobile: device.touch, hasTouch: device.touch })
    await context.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort())
    const page = await context.newPage()
    lastPage = page
    page.setDefaultTimeout(20000)
    readiness.watch(page, origin)
    for (const game of games) {
      const previous = receipt.journeys.find(row => row.device === device.name && row.game === game)
      if (previous?.states.includes('reload')) continue
      const record = previous ?? { device: device.name, game, states: [] }
      if (!previous) receipt.journeys.push(record)
      if (!record.path) {
      await readiness.goto(page, origin)
      await page.getByRole('link', { name: 'Choose ' + game, exact: true }).click()
      const dialog = page.getByRole('dialog', { name: 'New ' + game, exact: true })
      await dialog.waitFor({ state: 'visible' })
      await bounded(page, '.setup-dialog')
      const more = dialog.locator('details.setup-more')
      assert.equal(await more.getAttribute('open'), null, 'default More options should be closed: ' + game)
      if (game === 'Sudoku') assert.equal(await dialog.getByRole('group', { name: 'Board size', exact: true }).count(), 0)
      else {
        const groups = dialog.locator('fieldset').filter({ has: page.locator('legend').filter({ hasText: /^(Board size|Total board size, including blocks)$/ }) })
        await groups.getByRole('button').nth(expectedSizes[game].length - 1).waitFor({ state: 'visible' })
        const labels = await groups.getByRole('button').allTextContents()
        assert.deepEqual(labels.map(t => Number(t.trim().split(' ')[0])), expectedSizes[game], 'main sizes: ' + game)
      }
      await more.locator('summary').click()
      assert.equal(await more.getAttribute('open'), '', 'More options did not expand')
      if (game === 'Suguru') {
        assert.ok(await more.getByRole('button', { name: '10 × 15', exact: true }).isVisible())
        assert.ok(await more.getByRole('button', { name: '6', exact: true }).isVisible())
      }
      await more.locator('summary').click()
      await page.mouse.click(2, 2)
      await dialog.waitFor({ state: 'hidden' })
      record.states.push('main presets, secondary options, backdrop close')
      if (device.name === 'desktop') {
        await page.getByRole('link', { name: 'Choose ' + game, exact: true }).click()
        await dialog.waitFor({ state: 'visible' })
        const play = dialog.getByRole('button', { name: 'Play ' + game, exact: true })
        await play.waitFor({ state: 'visible' })
        assert.ok(await play.isEnabled(), 'default unavailable: ' + game)
        const response = page.waitForResponse(r => r.url() === origin + '/api/games/next')
        await play.click()
        const next = await response
        assert.equal(next.status(), 200, 'New: ' + game)
        await page.waitForURL(url => url.pathname.startsWith('/' + game.toLowerCase() + '/'))
        paths[game] = new URL(page.url()).pathname
        record.path = paths[game]
        record.states.push('New generated on demand')
      } else {
        await readiness.goto(page, origin + paths[game])
        record.states.push('actual native reopen')
      }
      } else await readiness.goto(page, origin + record.path)
      await page.locator('.player-board-frame').first().waitFor({ state: 'visible' })
      record.bounds = await bounded(page, '.player-board-frame')
      const board = page.locator('.player-board-frame').first()
      const cell = board.locator('button').first()
      if (await cell.count()) {
        if (device.touch) await cell.tap(); else await cell.click()
        await page.keyboard.press('ArrowRight')
      }
      const hint = page.getByRole('button', { name: 'A little hint', exact: true })
      if (await hint.count()) {
        await page.keyboard.press('Escape')
        assert.ok(await hint.isEnabled(), 'hint disabled: ' + game)
        const response = page.waitForResponse(r => new URL(r.url()).pathname === '/api/games/hint' || r.request().method() === 'POST' && r.request().postDataJSON()?.kind === 'hint')
        await hint.click()
        const hinted = await response
        assert.equal(hinted.status(), 200, 'hint HTTP: ' + game)
        await page.getByRole('heading', { name: 'Why this move?', exact: true }).waitFor({ state: 'visible' })
        record.states.push('checked hint; reason visible before reveal')
      }
      await page.getByRole('button', { name: 'Print board', exact: true }).click()
      await page.locator('iframe[title="Printable board preview"]').waitFor({ state: 'visible' })
      const frame = page.frameLocator('iframe[title="Printable board preview"]')
      await frame.locator('table, svg').first().waitFor({ state: 'visible' })
      await page.getByRole('button', { name: 'Close preview', exact: true }).click()
      record.states.push('print preview')
      await readiness.reload(page)
      await page.locator('.player-board-frame').first().waitFor({ state: 'visible' })
      await bounded(page, '.player-board-frame')
      record.states.push('reload')
      await page.screenshot({ path: output + '/' + device.name + '-' + game.toLowerCase() + '.png', fullPage: true })
      fs.writeFileSync(output + '/receipt.json', JSON.stringify(receipt, null, 2))
    }
    readiness.assertNoErrors(page)
    await browser.close(); browser = undefined
  }
  receipt.complete = true
} catch (error) {
  receipt.failure = error.stack
  if (lastPage) {
    await lastPage.screenshot({ path: output + '/failure.png', fullPage: true })
    receipt.failureText = (await lastPage.locator('body').innerText()).slice(-8000)
  }
  throw error
} finally {
  if (browser) await browser.close()
  fs.writeFileSync(output + '/receipt.json', JSON.stringify(receipt, null, 2))
}
console.log(JSON.stringify({ complete: true, journeys: receipt.journeys.length }))
