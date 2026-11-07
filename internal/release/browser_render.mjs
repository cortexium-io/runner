import assert from 'node:assert/strict'
import fs from 'node:fs'
import { createHash } from 'node:crypto'

export class BrowserResourceError extends Error {
  name = 'BrowserResourceError'
}

export class BrowserAdmissionError extends Error {
  name = 'BrowserAdmissionError'
}

// Only the standard delay header is read; its original contents never enter
// evidence. Missing headers use one conservative second after an explicit 429.
export function admissionDelay(value, now = Date.now()) {
  if (value === null) return { retryAfterMs: null, delayMs: 1000 }
  if (typeof value !== 'string' || value.length > 128) return null
  let delayMs
  if (/^\d{1,3}$/.test(value)) delayMs = Number(value) * 1000
  else {
    const date = new Date(value)
    if (!Number.isFinite(date.getTime()) || date.toUTCString() !== value) return null
    delayMs = Math.max(0, date.getTime() - now)
  }
  return delayMs <= 30000 ? { retryAfterMs: delayMs, delayMs } : null
}

// Full retry evidence has its own bounded file, inside the existing archive cap.
// Keep the receipt small and bind the complete audit, including an empty one.
export function saveBrowserReceipt(output, receipt, backoffs) {
  if (!Array.isArray(backoffs) || backoffs.length > 1024) throw new BrowserAdmissionError('admission backoff audit exceeds its record bound')
  const audit = JSON.stringify(backoffs), bytes = Buffer.byteLength(audit)
  if (bytes > 1024 * 1024) throw new BrowserAdmissionError('admission backoff audit exceeds its byte bound')
  const descriptor = { count: backoffs.length, path: 'admission-backoffs.json', bytes, sha256: createHash('sha256').update(audit).digest('hex') }
  const data = JSON.stringify({ ...receipt, admissionBackoffs: descriptor })
  if (Buffer.byteLength(data) > 128 * 1024) throw new BrowserAdmissionError('browser receipt exceeds its byte bound')
  fs.writeFileSync(output + '/' + descriptor.path, audit)
  fs.writeFileSync(output + '/receipt.json', data)
  receipt.admissionBackoffs = descriptor
}

// A missing stylesheet is transport/readiness evidence, never a layout verdict.
// Only navigation is retried; puzzle creation, hints and assertions are not.
export class PageReadiness {
  constructor(receipt, deadlineMs = null) {
    if (deadlineMs !== null && (!Number.isSafeInteger(deadlineMs) || deadlineMs <= 0)) throw new BrowserAdmissionError('Browser navigation deadline is invalid')
    this.receipt = receipt; this.deadlineMs = deadlineMs
    this.admissionBackoffs = []
    this.failures = new WeakMap(); this.errors = new WeakMap(); this.origins = new WeakMap()
  }

  watch(page, origin) {
    this.origins.set(page, origin)
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

  // The initial callback may enter a saved board once. A retry performs only the
  // identical canonical GET; no caller action or assertion is invoked again.
  async board(page, url, enter = () => page.goto(url, { waitUntil: 'load' })) {
    const canonical = new URL(url), origin = this.origins.get(page)
    const match = canonical.origin === origin && !canonical.search && !canonical.hash && canonical.pathname.length <= 256 && /^\/(sudoku|suguru|nonogram|binary|futoshiki|shikaku|hashi|calcudoku|kakuro)\/[A-Za-z0-9_-]+$/.exec(canonical.pathname)
    const backoffs = []
    const errorStart = this.errors.get(page).length
    let resourceRetried = false, rateRetried = false
    for (let attempt = 0; attempt < 3; attempt++) {
      if (attempt > 0 && this.deadlineMs !== null && Date.now() >= this.deadlineMs) throw new BrowserAdmissionError('browser deadline elapsed before canonical navigation recovery')
      let admission, signal
      const requests = new Set()
      const rejected = new Promise(resolve => { signal = resolve })
      const expected = request => {
        const target = new URL(request.url())
        return match && target.origin === origin && request.frame() === page.mainFrame() && (request.method() === 'GET' && request.resourceType() === 'document' && target.href === canonical.href || request.method() === 'POST' && target.pathname === '/api/games/' + match[1] && !target.search && page.url() === canonical.href)
      }
      const request = request => { if (expected(request)) requests.add(request) }
      const response = response => {
        const request = response.request()
        if (!admission && requests.has(request) && response.status() >= 400) {
          admission = { method: request.method(), path: new URL(request.url()).pathname, status: response.status(), document: request.resourceType() === 'document', delay: response.status() === 429 ? response.headerValue('retry-after').then(value => admissionDelay(value)).catch(() => null) : null }
          signal()
        }
      }
      const failed = request => {
        if (!admission && requests.has(request)) {
          admission = { method: request.method(), path: new URL(request.url()).pathname, status: null }
          signal()
        }
      }
      page.on('request', request); page.on('response', response); page.on('requestfailed', failed)
      try {
        this.failures.set(page, [])
        await (attempt === 0 ? enter() : page.goto(canonical.href, { waitUntil: 'load' }))
        // A rejected main document need not contain the product stylesheet.
        if (!admission?.document) await this.ready(page)
        if (!admission) await Promise.race([page.locator('.player-board-frame:visible').first().waitFor({ state: 'visible' }), rejected])
        if (!admission) {
          assert.equal(page.url(), canonical.href, 'board navigation changed canonical identity')
          return
        }
      } catch (error) {
        if (!(error instanceof BrowserResourceError) || resourceRetried || admission || !match || page.url() !== canonical.href || !this.failures.get(page).length) throw error
        resourceRetried = true
        this.receipt.resourceRetries ??= []
        this.receipt.resourceRetries.push({ url: canonical.href, failures: [...this.failures.get(page)], scriptErrors: this.errors.get(page).splice(errorStart) })
        continue
      } finally {
        page.off('request', request); page.off('response', response); page.off('requestfailed', failed)
      }
      let issue = 'canonical board admission failed'
      if (admission?.status === 429 && !rateRetried) {
        const delay = await admission.delay
        if (page.url() !== canonical.href) issue = 'canonical identity changed during rate-limited navigation'
        else if (!delay) issue = 'invalid or excessive Retry-After'
        else if (this.deadlineMs === null || Date.now() + delay.delayMs >= this.deadlineMs) issue = 'rate-limit backoff exceeds the known browser deadline'
        else if (this.admissionBackoffs.length >= 1024) issue = 'rate-limit backoff evidence exceeds its bound'
        else {
          rateRetried = true
          const evidence = { method: admission.method, path: admission.path, status: 429, ...delay, attempt: 1 }
          backoffs.push(evidence)
          this.admissionBackoffs.push({ ...evidence, canonicalPath: canonical.pathname })
          await new Promise(resolve => setTimeout(resolve, delay.delayMs))
          continue
        }
      } else if (admission?.status === 429) issue = 'canonical board remains rate limited after one retry'
      const error = new BrowserAdmissionError(issue + `: ${admission.method} ${admission.path} HTTP ${admission.status ?? 'unavailable'}`)
      error.browserDiagnostic = { status: 'partial', kind: 'admission', phase: 'canonical navigation', path: canonical.pathname, reload: { status: admission?.status === null ? 'partial' : 'complete', path: canonical.pathname, documentStatus: admission?.document ? admission.status : null, responses: admission ? [{ method: admission.method, path: admission.path, status: admission.status, outcome: admission.status === null ? 'request-failed' : 'http-error' }] : [], backoffs, issue } }
      throw error
    }
    throw new BrowserAdmissionError('canonical board navigation exhausted its resource/admission recovery bounds')
  }

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
