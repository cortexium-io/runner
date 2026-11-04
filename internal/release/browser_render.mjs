import assert from 'node:assert/strict'

export class BrowserResourceError extends Error {
  name = 'BrowserResourceError'
}

// A missing stylesheet is transport/readiness evidence, never a layout verdict.
// Only navigation is retried; puzzle creation, hints and assertions are not.
export class PageReadiness {
  constructor(receipt) { this.receipt = receipt; this.failures = new WeakMap(); this.errors = new WeakMap() }

  watch(page, origin) {
    this.failures.set(page, [])
    this.errors.set(page, [])
    page.on('pageerror', error => this.errors.get(page).push(error.message))
    page.on('requestfailed', request => {
      if (new URL(request.url()).origin !== origin || !['stylesheet', 'script', 'font'].includes(request.resourceType())) return
      const failure = { path: new URL(request.url()).pathname, error: request.failure()?.errorText }
      this.receipt.resourceFailures ??= []
      this.receipt.resourceFailures.push(failure)
      if (request.frame() === page.mainFrame() && failure.error !== 'net::ERR_ABORTED') this.failures.get(page).push(failure)
    })
  }

  async ready(page) {
    await page.waitForLoadState('load')
    const failures = this.failures.get(page) ?? []
    if (failures.length) throw new BrowserResourceError('Critical page resource failed: ' + JSON.stringify(failures))
    try {
      await page.waitForFunction(() => {
        const links = [...document.querySelectorAll('link[rel="stylesheet"]')].filter(link => !link.disabled)
        return links.length > 0 && links.every(link => link.sheet !== null)
      })
      // Force styled layout before waiting for the fonts that layout selected.
      await page.locator('body').boundingBox()
      await page.evaluate(() => document.fonts.ready.then(() => undefined))
    } catch (error) {
      throw new BrowserResourceError('Page styles/fonts did not become ready: ' + error.message)
    }
    if (failures.length) throw new BrowserResourceError('Critical page resource failed: ' + JSON.stringify(failures))
  }

  async load(page, navigate) {
    const errorStart = this.errors.get(page).length
    for (let attempt = 0; attempt < 2; attempt++) {
      this.failures.set(page, [])
      await navigate()
      try { await this.ready(page); return } catch (error) {
        if (!(error instanceof BrowserResourceError) || attempt !== 0 || !this.failures.get(page).length) throw error
        this.receipt.resourceRetries ??= []
        this.receipt.resourceRetries.push({ url: page.url(), failures: [...this.failures.get(page)], scriptErrors: this.errors.get(page).splice(errorStart) })
      }
    }
  }

  goto(page, url) { return this.load(page, () => page.goto(url, { waitUntil: 'load' })) }
  reload(page) { return this.load(page, () => page.reload({ waitUntil: 'load' })) }
  assertNoErrors(page) { assert.deepEqual(this.errors.get(page), [], 'browser errors') }

  async bounded(page, selector) {
    await this.ready(page)
    const bounds = await page.locator(selector).first().boundingBox()
    assert.ok(bounds, 'missing ' + selector)
    const metrics = await page.evaluate(() => ({ width: innerWidth, scroll: document.documentElement.scrollWidth }))
    assert.ok(metrics.scroll <= metrics.width + 1, 'page overflow: ' + JSON.stringify(metrics))
    assert.ok(bounds.x >= -1 && bounds.x + bounds.width <= metrics.width + 1, 'frame overflow: ' + JSON.stringify(bounds))
    return { ...bounds, viewportWidth: metrics.width, pageScrollWidth: metrics.scroll }
  }
}
