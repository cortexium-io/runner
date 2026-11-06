import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { isDeepStrictEqual } from 'node:util'

export const contractVersion = 2
export const games = ['Sudoku', 'Suguru', 'Nonogram', 'Binary', 'Futoshiki', 'Shikaku', 'Hashi', 'Calcudoku', 'Kakuro']
export const mobileViewports = [{ width: 320, height: 568 }, { width: 360, height: 640 }, { width: 390, height: 844 }]
export const boardSelector = '.puzzle-board,.shikaku-board,.nonogram-board,.binary-board,.futoshiki-board,.hashi-board,.calcudoku-board,.kakuro-board'

export function geometryKey(choice, nonogram) {
  const { game, size = 0, rows = 0, columns = 0, maximum = 0 } = choice
  assert.ok(games.some(name => name.toLowerCase() === game), 'unknown geometry game')
  assert.ok([size, rows, columns].every(n => Number.isInteger(n) && n >= 0 && n <= 30), 'invalid geometry dimensions')
  let key = `${game}:${size}:${rows}:${columns}`
  if (game === 'suguru') {
    assert.ok([4, 5, 6].includes(maximum), 'missing Suguru control maximum')
    key += `:max:${maximum}`
  }
  if (game === 'nonogram') {
    assert.ok(nonogram && Number.isInteger(nonogram.rowTextUnits) && nonogram.rowTextUnits >= 1 && nonogram.rowTextUnits <= size * 3 && Number.isInteger(nonogram.columnClues) && nonogram.columnClues >= 1 && nonogram.columnClues <= Math.ceil(size / 2), 'missing validated Nonogram clue footprint')
    key += `:clues:${nonogram.rowTextUnits}:${nonogram.columnClues}`
  }
  return key
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
    const key = geometryKey(row.choice, row.nonogramGeometry)
    if (!cases.has(key)) cases.set(key, { key, game: row.choice.game, path: row.path, size: row.choice.size })
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

export async function renderedState(page) {
  return page.locator('.player-board-frame:visible').first().evaluate((frame, boardSelector) => {
    // Compare persistent player content. Check feedback is deliberately transient
    // after admission/reload; no other descendant or class is discarded.
    const visible = node => {
      if (!node.getClientRects().length) return false
      for (let parent = node; parent; parent = parent.parentElement) {
        const style = getComputedStyle(parent)
        if (style.display === 'none' || style.visibility !== 'visible' || Number(style.opacity) === 0) return false
      }
      return true
    }
    const text = node => {
      const walker = document.createTreeWalker(node, NodeFilter.SHOW_TEXT)
      let all = '', shown = '', child
      while ((child = walker.nextNode())) {
        const parent = child.parentElement
        const decoration = parent.closest('.wrong-mark')
        if (decoration && decoration !== node && node.contains(decoration)) continue
        all += child.textContent
        const style = getComputedStyle(parent), range = document.createRange()
        range.selectNodeContents(child)
        const transparent = style.color === 'transparent' || /^rgba\([^)]*,\s*0(?:\.0+)?\s*\)$/.test(style.color) || /\/\s*0(?:\.0+)?%?\s*\)$/.test(style.color)
        if (visible(parent) && parseFloat(style.fontSize) > 0 && !transparent && [...range.getClientRects()].some(r => r.width > 0 && r.height > 0)) shown += child.textContent
      }
      return { primaryText: all.trim(), text: shown.trim() }
    }
    return {
      cells: [...frame.querySelectorAll('button')].map((node, index) => {
        const board = node.closest(boardSelector), kind = board?.classList ? [...board.classList].find(name => boardSelector.split(',').includes('.' + name)) : 'board'
        const attribute = node.hasAttribute('data-cell') ? 'data-cell' : node.hasAttribute('data-island') ? 'data-island' : null
        const cell = attribute ? node.getAttribute(attribute) : String(index)
        const escaped = /^[A-Za-z0-9_-]+$/.test(cell) ? cell : CSS.escape(cell)
        return { identity: `${kind}:${attribute ?? 'button'}:${cell}`, selector: attribute ? `.${kind} [${attribute}="${escaped}"]` : `.${kind} button:nth-of-type(${index + 1})`, cell, present: true, ...text(node), value: node.getAttribute('data-value'), fill: node.style.getPropertyValue('--rectangle-fill'), visible: visible(node) }
      }),
      bridges: [...frame.querySelectorAll('.hashi-board svg g')].map(node => ({ lines: node.querySelectorAll('line').length, visible: [...node.querySelectorAll('line')].filter(visible).length }))
    }
  }, boardSelector)
}

// Keep the complete comparison strict, while publishing only the first bounded
// semantic mismatch. Saved snapshots are represented by digests, never contents.
function stateFailure(message, diagnostic) {
  const error = new assert.AssertionError({ message, operator: 'deepStrictEqual' })
  error.browserDiagnostic = diagnostic
  throw error
}

export function assertRenderedState(actual, expected, message, context = {}) {
  if (isDeepStrictEqual(actual, expected)) return
  let differences = 0, first
  for (const field of ['cells', 'bridges']) {
    for (let i = 0; i < Math.max(actual[field].length, expected[field].length); i++) {
      if (isDeepStrictEqual(actual[field][i], expected[field][i])) continue
      differences++
      first ??= { field, index: i, actual: actual[field][i], expected: expected[field][i] }
    }
  }
  let truncated = differences > 1
  const bound = value => {
    if (typeof value !== 'string' || value.length <= 256) return value
    truncated = true
    return value.slice(0, 256)
  }
  const semantic = value => value ? Object.fromEntries(Object.entries(value).filter(([key]) => !['selector', 'cell'].includes(key)).map(([key, value]) => [key, bound(value)])) : null
  const expectedCell = semantic(first.expected), actualCell = semantic(first.actual)
  const selector = bound(first.expected?.selector ?? first.actual?.selector ?? '.hashi-board svg g')
  const cell = bound(first.expected?.cell ?? first.actual?.cell ?? String(first.index))
  stateFailure(message, { ...context, status: truncated ? 'truncated' : 'complete', kind: 'visible-state', selector, cell, expected: expectedCell, actual: actualCell, differences })
}

export function assertSavedState(actual, expected, message, context = {}) {
  if (isDeepStrictEqual(actual, expected)) return
  const digest = value => ({ sha256: createHash('sha256').update(JSON.stringify(value)).digest('hex'), keys: value.length })
  stateFailure(message, { ...context, status: 'complete', kind: 'saved-state', expected: digest(expected), actual: digest(actual) })
}

// Observe only admission transport facts. No response body, query, storage or
// credential data is read. Resource readiness keeps its existing classification.
export async function observeAdmission(page, phase, action) {
  const origin = new URL(page.url()).origin, rows = new Map()
  let truncated = false
  const request = req => {
    const url = new URL(req.url()), document = req.resourceType() === 'document' && req.frame() === page.mainFrame()
    if (url.origin !== origin || !document && !url.pathname.startsWith('/api/')) return
    if (rows.size === 8) { truncated = true; return }
    if (url.pathname.length > 256) truncated = true
    rows.set(req, { method: req.method(), path: url.pathname.slice(0, 256), resourceType: req.resourceType(), status: null, outcome: 'pending' })
  }
  const response = res => {
    const row = rows.get(res.request())
    if (row) { row.status = res.status(); row.outcome = res.ok() ? 'http-ok' : 'http-error' }
  }
  const failed = req => { const row = rows.get(req); if (row) row.outcome = 'request-failed' }
  page.on('request', request); page.on('response', response); page.on('requestfailed', failed)
  try { return await action() } catch (error) {
    const responses = [...rows.values()], path = new URL(page.url()).pathname.slice(0, 256)
    const reload = { ...error.browserDiagnostic?.reload, status: truncated ? 'truncated' : !responses.length ? 'missing' : responses.some(row => row.outcome === 'pending') ? 'partial' : 'complete', path, documentStatus: responses.findLast(row => row.resourceType === 'document')?.status ?? null, responses }
    error.browserDiagnostic ??= { status: 'partial', kind: 'reload', phase, path }
    if (error.browserDiagnostic.kind === 'admission') error.browserDiagnostic.phase = phase
    error.browserDiagnostic.reload = reload
    throw error
  } finally {
    page.off('request', request); page.off('response', response); page.off('requestfailed', failed)
  }
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
  for (const [point, cell] of [[start, first], [end, last]]) assert.equal(await page.evaluate(({ point }) => document.elementFromPoint(point.x,point.y)?.closest('[data-cell]')?.getAttribute('data-cell'), { point }),String(cell),'Shikaku corner is clipped or covered')
  if (touch && engine === 'chromium') {
    const cdp = await page.context().newCDPSession(page)
    try {
      // The browser's native gesture controller completes the drag and prevents
      // a fling. Raw touch packets can leave subsequent tap clicks suppressed.
      await cdp.send('Input.synthesizeScrollGesture', { ...start, xDistance: end.x - start.x, yDistance: end.y - start.y, gestureSourceType: 'touch', speed: 400, preventFling: true })
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

export async function prepareShikakuTouch(page) {
  const cells = page.locator('.shikaku-board:visible [data-cell]')
  for (let i=0;i<8;i++) {
    const box = await cells.first().boundingBox()
    if (Math.min(box.width,box.height)>=32) break
    const zoom = page.getByRole('button',{name:'Zoom in',exact:true})
    assert.equal(await zoom.count(),1,'large Shikaku needs a visible usable Zoom in control for touch editing')
    await activate(page,zoom,true)
    try { await page.waitForFunction(previous => document.querySelector('.shikaku-board [data-cell]')?.getBoundingClientRect().width > previous+0.5,box.width) }
    catch(error){if(error.name!=='TimeoutError')throw error;assert.fail('Shikaku zoom did not enlarge actual cells')}
  }
  const pair = await cells.evaluateAll(nodes => {
    const n=Math.sqrt(nodes.length),frame=nodes[0]?.closest('.player-board-frame')?.getBoundingClientRect()
    const visible=node=>{const r=node.getBoundingClientRect();return Math.min(r.width,r.height)>=32&&r.left>=Math.max(0,frame.left)-1&&r.top>=Math.max(0,frame.top)-1&&r.right<=Math.min(innerWidth,frame.right)+1&&r.bottom<=Math.min(innerHeight,frame.bottom)+1&&document.elementFromPoint(r.x+r.width/2,r.y+r.height/2)?.closest('[data-cell]')===node}
    if(!Number.isInteger(n)||!frame)return
    for(let first=0;first<nodes.length;first++){const last=first+n+1;if(first%n<n-1&&last<nodes.length&&visible(nodes[first])&&visible(nodes[last]))return{first,last}}
  })
  assert.ok(pair,'large Shikaku has no reachable 2×2 touch region after bounded zoom')
  return pair
}

export async function editAndRestore(page, game, touch, engine, readiness, corners = { first: 0, last: 6 }) {
  const before = await savedState(page)
  const inputBefore = await page.evaluate(() => window.runnerInputEvidence)
  const frame = page.locator('.player-board-frame:visible').first()
  let paint
  const paintBefore=game==='Shikaku'?await frame.locator('[data-cell]').evaluateAll(nodes=>nodes.map(node=>getComputedStyle(node).backgroundColor)):null
  const scrollBefore=await page.evaluate(()=>[scrollX,scrollY])
  if (game === 'Shikaku') await dragRectangle(page, touch, engine,corners.first,corners.last)
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
  if(game==='Shikaku'){
    const grid=Math.sqrt(await frame.locator('[data-cell]').count()),expected=[]
    assert.ok(Number.isInteger(grid),'Shikaku cell grid is not square')
    for(let row=Math.min(Math.floor(corners.first/grid),Math.floor(corners.last/grid));row<=Math.max(Math.floor(corners.first/grid),Math.floor(corners.last/grid));row++)for(let col=Math.min(corners.first%grid,corners.last%grid);col<=Math.max(corners.first%grid,corners.last%grid);col++)expected.push(row*grid+col)
    const scrollAfter=await page.evaluate(()=>[scrollX,scrollY])
    if(touch)assert.ok(scrollAfter.every((value,i)=>Math.abs(value-scrollBefore[i])<=1),'Shikaku touch rectangle panned the page')
    try { await page.waitForFunction(({expected,paintBefore})=>{
      const cells=[...document.querySelectorAll('.shikaku-board [data-cell]')]
      const painted=cells.filter(node=>node.style.getPropertyValue('--rectangle-fill').trim()).map(node=>Number(node.dataset.cell)).sort((a,b)=>a-b)
      return JSON.stringify(painted)===JSON.stringify(expected)&&expected.every(i=>getComputedStyle(cells[i]).backgroundColor!==paintBefore[i])
    },{expected,paintBefore}) }catch(error){if(error.name!=='TimeoutError')throw error;const actual=await frame.locator('[data-cell]').evaluateAll(nodes=>nodes.filter(node=>node.style.getPropertyValue('--rectangle-fill').trim()).map(node=>Number(node.dataset.cell)));assert.fail(`Shikaku painted cells differ: expected ${expected.slice(0,20)}; actual ${actual.slice(0,20)}`)}
    paint={grid,...corners,cells:expected,scroll:[...scrollBefore,...scrollAfter]}
  }
  const edited = await savedState(page)
  const rendered = await renderedState(page)
  await activate(page, page.getByRole('button', { name: 'Undo', exact: true }), touch, touch)
  await waitState(page, before, false)
  await activate(page, page.getByRole('button', { name: 'Redo', exact: true }), touch, touch)
  await waitState(page, edited, false)
  const input = await page.evaluate(() => window.runnerInputEvidence)
  if (touch) assert.ok(input.touch > inputBefore.touch && input.keyboard === 0, 'mobile edit did not use trusted touch-only input')
  await observeAdmission(page, 'reload', async () => {
    await readiness.board(page, page.url(), () => page.reload({ waitUntil: 'load' }))
    await verifyPlayArea(page, touch)
    const context = { phase: 'reload', path: new URL(page.url()).pathname }
    assertSavedState(await savedState(page), edited, 'reload changed saved puzzle state', context)
    assertRenderedState(await renderedState(page), rendered, 'reload did not restore the visible board edit', context)
  })
  return { input, paint, touchDrag: touch && game === 'Shikaku' ? engine === 'chromium' ? 'native-verified' : 'native-taps-verified; WebKit drag API unavailable' : undefined }
}
