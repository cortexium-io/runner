import assert from 'node:assert/strict'

export const contractVersion = 2
export const games = ['Sudoku', 'Suguru', 'Nonogram', 'Binary', 'Futoshiki', 'Shikaku', 'Hashi', 'Calcudoku', 'Kakuro']
export const mobileViewports = [{ width: 320, height: 568 }, { width: 360, height: 640 }, { width: 390, height: 844 }]
export const boardSelector = '.puzzle-board,.shikaku-board,.nonogram-board,.binary-board,.futoshiki-board,.hashi-board,.calcudoku-board,.kakuro-board'

export function geometryKey(choice) {
  const { game, size = 0, rows = 0, columns = 0 } = choice
  assert.ok(games.some(name => name.toLowerCase() === game), 'unknown geometry game')
  assert.ok([size, rows, columns].every(n => Number.isInteger(n) && n >= 0 && n <= 30), 'invalid geometry dimensions')
  return `${game}:${size}:${rows}:${columns}`
}

// Reuse the actual paths from the native catalogue proof, not fixtures or guessed
// identities. Difficulty/density is not multiplied when board geometry is equal.
export function geometryCases(runtime) {
  assert.equal(runtime.status, 'passed', 'native catalogue proof did not pass')
  assert.ok(runtime.choices > 0 && runtime.choices === runtime.passed && runtime.observations?.length === runtime.choices, 'incomplete native catalogue proof')
  const cases = new Map()
  for (const row of runtime.observations) {
    assert.equal(row.identicalReopen, true, 'native publication did not reopen')
    assert.ok(!row.error && typeof row.path === 'string' && new RegExp(`^/${row.choice.game}/[A-Za-z0-9_-]+$`).test(row.path), 'missing source-bound catalogue path')
    const key = geometryKey(row.choice)
    if (!cases.has(key)) cases.set(key, { key, game: row.choice.game, path: row.path })
  }
  assert.equal(new Set([...cases.values()].map(row => row.game)).size, games.length, 'catalogue omits an interaction family')
  return [...cases.values()]
}

function within(box, viewport) {
  return box && box.width > 0 && box.height > 0 && box.x >= -1 && box.y >= -1 && box.x + box.width <= viewport.width + 1 && box.y + box.height <= viewport.height + 1
}

export function assertPlayArea(metrics, { mobile = true } = {}) {
  assert.ok(metrics.board && metrics.frame, 'missing actual board/frame')
  assert.ok(metrics.documentWidth <= metrics.viewport.width + 1, 'document overflows the viewport')
  assert.ok(within(metrics.frame, { width: metrics.viewport.width, height: Number.MAX_SAFE_INTEGER }), 'outer frame overflows horizontally')
  assert.ok(metrics.board.x >= metrics.frame.x - 1 && metrics.board.x + metrics.board.width <= metrics.frame.x + metrics.frame.width + 1, 'actual board is clipped inside its frame')
  assert.ok(metrics.board.y >= metrics.frame.y - 1 && metrics.board.y + metrics.board.height <= metrics.frame.y + metrics.frame.height + 1, 'actual board is vertically clipped inside its frame')
  assert.ok(metrics.internalWidth <= metrics.clientWidth + 1 && metrics.internalHeight <= metrics.clientHeight + 1, 'actual board has internal overflow')
  if (mobile) {
    assert.ok(within(metrics.board, metrics.viewport), 'actual board is outside the initial play viewport')
    assert.ok(metrics.controls.some(row => row.label === 'Undo'), 'missing primary Undo control')
    for (const control of metrics.controls) assert.ok(within(control, metrics.viewport), `primary control is offscreen: ${control.label}`)
  }
  return metrics
}

export async function playMetrics(page) {
  return page.evaluate(({ boardSelector }) => {
    const visible = node => node.getClientRects().length && getComputedStyle(node).visibility !== 'hidden'
    const rect = node => { const r = node.getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height } }
    const frame = [...document.querySelectorAll('.player-board-frame')].find(visible)
    const board = frame && [...frame.querySelectorAll(boardSelector)].find(visible)
    const controls = [...document.querySelectorAll('.number-pad button,.value-controls button,.rectangle-controls button,.player-actions button,button')]
      .filter(visible).filter(node => {
        const text = node.textContent.trim()
        return node.closest('.number-pad,.value-controls,.rectangle-controls') || /^(Undo|Redo|Check(?:…|\.\.\.)?|Add rectangle|Cancel preview)$/.test(text)
      }).map(node => ({ ...rect(node), label: node.textContent.trim() }))
    const fontSizes = board ? [...board.querySelectorAll('button,svg text,.row-clues span,.column-clues span')].filter(visible)
      .filter(node => [...node.childNodes].some(child => child.nodeType === Node.TEXT_NODE && /\d/.test(child.textContent)))
      .map(node => {
        const matrix = node instanceof SVGGraphicsElement ? node.getScreenCTM() : null
        const scale = matrix ? Math.hypot(matrix.a, matrix.b) : node.offsetWidth ? node.getBoundingClientRect().width / node.offsetWidth : 1
        return parseFloat(getComputedStyle(node).fontSize) * scale
      }) : []
    return { viewport: { width: innerWidth, height: innerHeight }, documentWidth: document.documentElement.scrollWidth, frame: frame && rect(frame), board: board && rect(board), internalWidth: board?.scrollWidth, internalHeight: board?.scrollHeight, clientWidth: board?.clientWidth, clientHeight: board?.clientHeight, controls, minClueFont: fontSizes.length ? Math.min(...fontSizes) : 12 }
  }, { boardSelector })
}

export async function activate(page, locator, touch, requireVisible = true) {
  if (requireVisible) {
    const box = await locator.boundingBox()
    const viewport = await page.evaluate(() => ({ width: innerWidth, height: innerHeight }))
    assert.ok(within(box, viewport), 'interaction would require automatic scrolling: ' + (await locator.getAttribute('aria-label') || await locator.textContent() || 'control'))
  }
  if (touch) await locator.tap(); else await locator.click()
}

export async function verifyPlayArea(page, touch) {
  const metrics = assertPlayArea(await playMetrics(page), { mobile: touch })
  if (touch && metrics.minClueFont < 12) {
    // A tiny overview is acceptable only with working visible zoom/fit controls.
    const zoom = page.getByRole('button', { name: 'Zoom in', exact: true })
    const fit = page.getByRole('button', { name: 'Fit board', exact: true })
    assert.equal(await zoom.count(), 1, 'small clues need an explicit usable Zoom in control')
    let enlarged = metrics
    for (let i = 0; i < 4 && enlarged.minClueFont < 12; i++) {
      await activate(page, zoom, true)
      enlarged = await playMetrics(page)
    }
    assert.ok(enlarged.minClueFont >= 12 && enlarged.board.width > metrics.board.width, 'zoom did not make actual clues readable')
    await activate(page, fit, true)
    assertPlayArea(await playMetrics(page))
    return { ...metrics, verifiedZoom: true }
  }
  return metrics
}

export async function savedState(page) {
  return page.evaluate(() => Object.keys(localStorage).sort().flatMap(key => {
    if (!key.startsWith('games-site:')) return []
    try { const value = JSON.parse(localStorage.getItem(key)); return value.snapshot ? [{ key, snapshot: value.snapshot }] : [] } catch { return [] }
  }))
}

async function renderedState(page) {
  return page.locator('.player-board-frame:visible').first().evaluate(frame => ({
    cells: [...frame.querySelectorAll('button')].map(node => ({ text: node.textContent.trim(), value: node.getAttribute('data-value'), fill: node.style.getPropertyValue('--rectangle-fill') })),
    bridges: [...frame.querySelectorAll('.hashi-board svg g')].map(node => node.querySelectorAll('line').length)
  }))
}

async function waitState(page, before, changed) {
  try {
    await page.waitForFunction(({ before, changed }) => {
      const state = Object.keys(localStorage).sort().flatMap(key => {
        if (!key.startsWith('games-site:')) return []
        try { const value = JSON.parse(localStorage.getItem(key)); return value.snapshot ? [{ key, snapshot: value.snapshot }] : [] } catch { return [] }
      })
      return (JSON.stringify(state) !== before) === changed
    }, { before: JSON.stringify(before), changed })
  } catch (error) {
    if (error.name !== 'TimeoutError') throw error
    assert.fail('required board action did not reach the expected saved state')
  }
}

export async function dragRectangle(page, touch, engine, first = 0, last = 6) {
  const board = page.locator('.shikaku-board:visible')
  const a = await board.locator(`[data-cell="${first}"]`).boundingBox()
  const b = await board.locator(`[data-cell="${last}"]`).boundingBox()
  const viewport = await page.evaluate(() => ({ width: innerWidth, height: innerHeight }))
  assert.ok(within(a, viewport) && within(b, viewport), 'rectangle corners are outside the play viewport')
  const start = { x: a.x + a.width / 2, y: a.y + a.height / 2 }, end = { x: b.x + b.width / 2, y: b.y + b.height / 2 }
  if (touch && engine === 'chromium') {
    const cdp = await page.context().newCDPSession(page)
    try {
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ ...start, id: 1 }] })
      for (let i = 1; i <= 8; i++) await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x: start.x + (end.x - start.x) * i / 8, y: start.y + (end.y - start.y) * i / 8, id: 1 }] })
      await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] })
    } finally { await cdp.detach() }
  } else if (touch) {
    // Playwright WebKit has native taps but no public native touch-drag API.
    // Test its supported complete two-corner path; report the drag limitation.
    await activate(page, board.locator(`[data-cell="${first}"]`), true)
    await activate(page, board.locator(`[data-cell="${last}"]`), true)
    await activate(page, page.getByRole('button', { name: 'Add rectangle', exact: true }), true)
  } else {
    await page.mouse.move(start.x, start.y); await page.mouse.down()
    await page.mouse.move(end.x, end.y, { steps: 8 }); await page.mouse.up()
  }
}

export async function editAndRestore(page, game, touch, engine, readiness) {
  const before = await savedState(page)
  const inputBefore = await page.evaluate(() => window.runnerInputEvidence)
  const frame = page.locator('.player-board-frame:visible').first()
  if (game === 'Shikaku') await dragRectangle(page, touch, engine)
  else if (game === 'Hashi') {
    const pair = await frame.locator('[data-island]').evaluateAll(nodes => {
      const islands = nodes.map(node => { const r = node.getBoundingClientRect(); return { id: node.getAttribute('data-island'), x: r.x + r.width / 2, y: r.y + r.height / 2 } })
      for (const a of islands) for (const b of islands) {
        const horizontal = Math.abs(a.y - b.y) < 1, vertical = Math.abs(a.x - b.x) < 1
        if (a === b || !horizontal && !vertical) continue
        const between = islands.some(c => c !== a && c !== b && (horizontal ? Math.abs(a.y - c.y) < 1 && c.x > Math.min(a.x, b.x) && c.x < Math.max(a.x, b.x) : Math.abs(a.x - c.x) < 1 && c.y > Math.min(a.y, b.y) && c.y < Math.max(a.y, b.y)))
        if (!between) return [a.id, b.id]
      }
    })
    assert.ok(pair, 'no selectable Hashi neighbour pair')
    for (const id of pair) await activate(page, frame.locator(`[data-island="${id}"]`), touch, touch)
    await activate(page, page.getByRole('button', { name: 'One bridge', exact: true }), touch, touch)
  } else {
    const candidates = frame.locator('button[data-cell]:not(.given):not([data-given="true"])')
    const cells = await candidates.all()
    const cell = game === 'Nonogram' || game === 'Binary' ? cells[0] : await (async () => { for (const c of cells) if (!(await c.textContent()).trim()) return c })()
    assert.ok(cell, 'no editable board cell')
    await activate(page, cell, touch, touch)
    const control = game === 'Nonogram' ? page.getByRole('button', { name: /Shade/ }) : page.locator('.number-pad button:visible,.value-controls button:visible').filter({ hasText: /^\s*1\s*$/ }).first()
    await activate(page, control, touch, touch)
  }
  await waitState(page, before, true)
  const edited = await savedState(page)
  const rendered = await renderedState(page)
  await activate(page, page.getByRole('button', { name: 'Undo', exact: true }), touch, touch)
  await waitState(page, before, false)
  await activate(page, page.getByRole('button', { name: 'Redo', exact: true }), touch, touch)
  await waitState(page, edited, false)
  const input = await page.evaluate(() => window.runnerInputEvidence)
  if (touch) assert.ok(input.touch > inputBefore.touch && input.keyboard === 0, 'mobile edit did not use trusted touch-only input')
  await readiness.reload(page)
  await page.locator('.player-board-frame:visible').first().waitFor({ state: 'visible' })
  await verifyPlayArea(page, touch)
  assert.deepEqual(await savedState(page), edited, 'reload changed saved puzzle state')
  assert.deepEqual(await renderedState(page), rendered, 'reload did not restore the visible board edit')
  return { input, touchDrag: touch && game === 'Shikaku' ? engine === 'chromium' ? 'native-verified' : 'native-taps-verified; WebKit drag API unavailable' : undefined }
}
