import fs from 'node:fs'
import assert from 'node:assert/strict'
import { captureViewport as captureExperienceViewport, settleViewport, BrowserCapabilityError, preferenceScenario, appearanceScenario, notesScenario, shikakuScenario, hintContinuityScenario, privacyScenario, completionRecipe, completeRepresentative } from './browser_experience.mjs'
import { pathToFileURL } from 'node:url'
import { PageReadiness, saveBrowserReceipt } from './browser_render.mjs'
import { contractVersion, games, mobileViewports, geometryCases, verifyPlayArea, activate, editAndRestore, playMetrics, prepareShikakuTouch, savedState, renderedState, assertSavedState, assertRenderedState, observeAdmission } from './browser_contract.mjs'
const { chromium, webkit } = await import(pathToFileURL(process.env.RUNNER_PLAYWRIGHT_MODULE).href)
const [origin, output] = process.argv.slice(2)
const expectedSizes = { Sudoku: [], Suguru: [6, 8, 10, 12], Nonogram: [5, 10, 15], Binary: [6, 8, 10], Futoshiki: [4, 5, 7], Shikaku: [5, 7, 10], Hashi: [7, 9, 10, 15], Calcudoku: [4, 5, 6, 9], Kakuro: [6, 10, 16] }
const cases = geometryCases(JSON.parse(fs.readFileSync(output + '/../runtime-http.json')))
const largeShikaku = cases.filter(row=>row.game==='shikaku').reduce((a,b)=>a.size>=b.size?a:b)
const receipt = { contract: contractVersion, origin, mockedResponses: false, headless: true, engines: [], journeys: [], geometry: [], complete: false, limitations: ['Headless touch emulation is not a physical iOS/Android device check.', 'Playwright WebKit exposes native taps but no public native touch-drag API; its complete two-corner Shikaku path is verified.'] }
receipt.viewportImages = []
receipt.experienceObservations = []
receipt.experienceGaps = ['Simulator Safari and physical Red Head rapid-tap zoom are unqualified.', 'Real CMP/vendor privacy states are not proved by first-party loopback UI or controlled fixtures; canonical origin and restricted vendor egress are unavailable.', 'WebKit trusted simultaneous-contact input is unsupported; sequential notes are separate proof.']
receipt.experienceScenarios = []
fs.mkdirSync(output, { recursive: true })
const readiness = new PageReadiness(receipt, process.env.RUNNER_BROWSER_DEADLINE_MS ? Number(process.env.RUNNER_BROWSER_DEADLINE_MS) : null)
const save = () => saveBrowserReceipt(output, receipt, readiness.admissionBackoffs)
const captureViewport = (...args) => captureExperienceViewport(receipt,output,...args)
const scenario = async (engine,game,name,action) => {
  receipt.current.phase='fixed scenario: '+name
  let proof
  try { proof=await action() } catch(error) {
    if(!(error instanceof BrowserCapabilityError))throw error
    proof={status:'blocked',gap:error.message}
  }
  receipt.experienceScenarios.push({engine,game,case:name,status:'passed',...proof})
  if(Buffer.byteLength(JSON.stringify(receipt.experienceScenarios))>12*1024)throw new BrowserCapabilityError('fixed scenario witnesses exceed 12 KiB within the receipt bound')
}
const largestCases = new Map()
for (const sample of cases) if (!largestCases.has(sample.game) || largestCases.get(sample.game).cells < sample.cells) largestCases.set(sample.game, sample)
const launch = engine => engine.type.launch({ executablePath: engine.executable, headless: true, args: engine.name === 'chromium' && process.platform === 'darwin' ? ['--disable-gpu', '--single-process', '--no-zygote'] : [] })
let profile = 0
const newProfile = (browser, options) => {
  assert.ok(++profile <= 64, 'browser profile address allocation exhausted')
  return browser.newContext({ ...options, extraHTTPHeaders: { 'X-Runner-Browser-Client': String(profile) } })
}
const instrument = context => context.addInitScript(() => {
  window.runnerInputEvidence={touch:0,keyboard:0}
  document.addEventListener('pointerdown',e=>{if(e.isTrusted&&e.pointerType==='touch')window.runnerInputEvidence.touch++},true)
  document.addEventListener('touchstart',e=>{if(e.isTrusted)window.runnerInputEvidence.touch++},true)
  document.addEventListener('keydown',e=>{if(e.isTrusted)window.runnerInputEvidence.keyboard++},true)
})
let browser, supportBrowser, lastPage
try {
  for (const engine of [{ name: 'chromium', type: chromium, executable: process.env.RUNNER_BROWSER_EXECUTABLE }, { name: 'webkit', type: webkit, executable: process.env.RUNNER_WEBKIT_EXECUTABLE }]) {
    if (!engine.executable) throw new Error(`Browser prerequisite missing: configured ${engine.name} executable`)
    receipt.engines.push(engine.name)
    for (const device of [{ name: 'desktop', width: 1440, height: 1000, touch: false }, { name: 'mobile', ...mobileViewports[0], touch: true }]) {
      // Restricted macOS Chromium requires single-process mode, which supports
      // only one browser context. Give each isolated profile its own process.
      browser = await launch(engine)
      const context = await newProfile(browser, { ignoreHTTPSErrors: true, viewport: { width: device.width, height: device.height }, isMobile: device.touch, hasTouch: device.touch })
      await instrument(context)
      await context.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort())
      const page = await context.newPage(); lastPage = page
      page.setDefaultTimeout(20000)
      readiness.watch(page, origin)
      for (const game of games) {
        receipt.current = { engine: engine.name, device: device.name, game, width: device.width, height: device.height, phase: 'open setup' }
        const record = { engine: engine.name, device: device.name, game, states: [] }
        receipt.journeys.push(record)
        await readiness.goto(page, origin)
        await activate(page, page.getByRole('link', { name: 'Choose ' + game, exact: true }), device.touch, false)
        const dialog = page.getByRole('dialog', { name: 'New ' + game, exact: true })
        await dialog.waitFor({ state: 'visible' })
        await readiness.bounded(page, '.setup-dialog')
        await page.waitForFunction(() => !document.body.innerText.includes('Checking puzzle settings…'))
        if (device.touch) {
          await page.setViewportSize({width:360,height:640}); await settleViewport(page)
          await page.emulateMedia({colorScheme:'dark'})
          await settleViewport(page)
          await captureViewport(page,engine.name,device.name,game,'chooser-dark')
          await page.emulateMedia({colorScheme:'light'})
          await page.setViewportSize({width:device.width,height:device.height}); await settleViewport(page)
        }
        const more = dialog.locator('details.setup-more')
        assert.equal(await more.getAttribute('open'), null, 'default More options should be closed: ' + game)
        const play = dialog.getByRole('button', { name: 'Play ' + game, exact: true })
        if (game === 'Sudoku') assert.equal(await dialog.getByRole('group', { name: 'Board size', exact: true }).count(), 0)
        else {
          receipt.current.phase = 'primary presets'
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
        receipt.current.phase = 'New entry'
        await observeAdmission(page, 'new-entry', () => readiness.newBoard(page, game.toLowerCase(), async () => {
          const [response] = await Promise.all([
            page.waitForResponse(r => r.url() === origin + '/api/games/next' && r.request().method() === 'POST'),
            activate(page, play, device.touch)
          ])
          assert.equal(response.status(), 200, 'New: ' + game)
          await page.waitForURL(url => url.pathname.startsWith('/' + game.toLowerCase() + '/'))
          record.path = new URL(page.url()).pathname
          receipt.current.path = record.path
        }))
        // This runs before any locator interaction can scroll the board into view.
        receipt.current.phase = 'initial board and controls'
        if (device.touch) await captureViewport(page,engine.name,device.name,game,'initial')
        const initial = await verifyPlayArea(page, device.touch)
        record.bounds = { viewport: initial.viewport, frame: initial.frame, board: initial.board, internal: [initial.internalWidth, initial.clientWidth, initial.internalHeight, initial.clientHeight], primaryControls: initial.controls.map(row => row.label), minClueFont: initial.minClueFont, verifiedZoom: initial.verifiedZoom === true }
        record.states.push('New enters usable initial play viewport')
        receipt.current.phase = 'touch edit, Undo, Redo, reload'
        record.edit = await editAndRestore(page, game, device.touch, engine.name, readiness)
        record.states.push('actual edit, Undo, Redo, saved visible state survives reload')
        receipt.current.phase = 'home saved-board resume'
        const resumeSave=await savedState(page),resumeView=await renderedState(page)
        await observeAdmission(page, 'home-resume', async () => {
          await readiness.goto(page,origin)
          await page.locator('.saved-row[data-board-id]').first().waitFor({state:'visible'})
          const code=record.path.split('/').at(-1),ids=[code,game.toLowerCase()+'-generated:'+code]
          let resume
          for(const row of await page.locator('.saved-row[data-board-id]').all())if(ids.includes(await row.getAttribute('data-board-id'))){resume=row.locator('.resume-button');break}
          assert.ok(resume,'home saved-board Resume control is missing for '+record.path)
          await readiness.board(page, origin + record.path, async () => {
            await activate(page,resume,device.touch,false)
            await page.waitForURL(url=>url.pathname===record.path)
          }, async () => {
            await verifyPlayArea(page,device.touch)
            const context = { phase: 'home-resume', path: new URL(page.url()).pathname }
            assertSavedState(await savedState(page),resumeSave,'home resume changed saved puzzle state',context)
            assertRenderedState(await renderedState(page),resumeView,'home resume did not restore the visible edit',context)
          })
        })
        record.resumeInput=await page.evaluate(()=>window.runnerInputEvidence)
        if(device.touch)assert.ok(record.resumeInput.touch>0&&record.resumeInput.keyboard===0,'home resume did not use trusted touch-only input')
        record.states.push('home resume restores saved visible edit in usable viewport')
        // A separate fresh profile keeps the deliberate arbitrary edit from
        // making an otherwise valid hint correctly request a recheck first.
        supportBrowser = await launch(engine)
        receipt.current.phase = 'hint and print support profile'
        const supportContext = await newProfile(supportBrowser, { ignoreHTTPSErrors: true, viewport: { width: device.width, height: device.height }, isMobile: device.touch, hasTouch: device.touch })
        await instrument(supportContext)
        await supportContext.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort())
        const support = await supportContext.newPage(); lastPage = support; support.setDefaultTimeout(20000); readiness.watch(support, origin)
        await readiness.board(support, origin + record.path)
        const hint = support.getByRole('button', { name: 'A little hint', exact: true })
        if (await hint.count()) {
          receipt.current.phase = 'hint'
          assert.ok(await hint.isEnabled(), 'hint disabled: ' + game)
          const response = support.waitForResponse(r => new URL(r.url()).pathname === '/api/games/hint' || r.request().method() === 'POST' && r.request().postDataJSON()?.kind === 'hint')
          await activate(support, hint, device.touch, false)
          assert.equal((await response).status(), 200, 'hint HTTP: ' + game)
          await support.getByRole('heading', { name: 'Why this move?', exact: true }).waitFor({ state: 'visible' })
          record.states.push('checked hint reason')
          if (device.touch && game !== 'Nonogram') await captureViewport(support,engine.name,device.name,game,'hint')
        }
        receipt.current.phase = 'print preview'
        await activate(support, support.getByRole('button', { name: 'Print board', exact: true }), device.touch, false)
        await support.locator('iframe[title="Printable board preview"]').waitFor({ state: 'visible' })
        await support.frameLocator('iframe[title="Printable board preview"]').locator('table, svg').first().waitFor({ state: 'visible' })
        await activate(support, support.getByRole('button', { name: 'Close preview', exact: true }), device.touch)
        record.states.push('print preview')
        if(device.touch){
          if(game==='Nonogram')await scenario(engine.name,game,'hint-continuity',()=>hintContinuityScenario(support,readiness,cases.find(row=>row.game==='binary')?.path,state=>captureViewport(support,engine.name,'mobile',game,state)))
          if(['Sudoku','Suguru'].includes(game))await scenario(engine.name,game,'entry-preferences',async()=>{
            const proof=await preferenceScenario(support,readiness)
            if(game==='Sudoku')proof.appearance=await appearanceScenario(support,readiness,()=>captureViewport(support,engine.name,'mobile',game,'preferences'))
            else await captureViewport(support,engine.name,'mobile',game,'preferences')
            return proof
          })
          if(['Sudoku','Suguru','Futoshiki','Calcudoku','Kakuro'].includes(game))await scenario(engine.name,game,'contact-notes',async()=>{
            const proof=await notesScenario(support,engine.name,readiness)
            await captureViewport(support,engine.name,'mobile',game,'notes')
            return proof
          })
          if(game==='Shikaku')await scenario(engine.name,game,'fresh-commits',async()=>{
            const proof=await shikakuScenario(support,readiness)
            await captureViewport(support,engine.name,'mobile',game,'commit')
            return proof
          })
          if(game==='Sudoku')await scenario(engine.name,game,'privacy',async()=>{
            const proof=await privacyScenario(support)
            await captureViewport(support,engine.name,'mobile',game,'privacy')
            return proof
          })
        }
        if(engine.name==='chromium'&&!device.touch)await scenario(engine.name,game,'representative-solved',async()=>{
          const recipe=await completionRecipe(support,game.toLowerCase(),readiness)
          return completeRepresentative(support,recipe,readiness,()=>captureViewport(support,engine.name,'desktop',game,'solved'))
        })
        readiness.assertNoErrors(support)
        await supportBrowser.close(); supportBrowser = undefined; lastPage = page
        receipt.current.phase = 'screenshot'
        if (device.touch) {
          const before=await savedState(page)
          await page.setViewportSize({width:844,height:390}); await settleViewport(page)
          await captureViewport(page,engine.name,'mobile',game,'landscape')
          await page.setViewportSize({width:768,height:1024}); await settleViewport(page)
          const tablet=await playMetrics(page)
          await page.setViewportSize({width:390,height:844}); await settleViewport(page)
          // The base crop is now the actual final tablet-to-phone viewport;
          // avoid a second copy of the same screen while reserving case images.
          await captureViewport(page,engine.name,'mobile',game)
          receipt.experienceObservations.at(-1).tabletBoard=tablet.board
          assertSavedState(await savedState(page),before,'viewport transitions changed saved progress',{phase:'experience viewport transition',path:record.path})
          await page.setViewportSize({width:device.width,height:device.height}); await settleViewport(page)
        } else await captureViewport(page,engine.name,device.name,game)
        await page.screenshot({ path: `${output}/${engine.name}-${device.name}-${game.toLowerCase()}.png`, fullPage: true })
        save()
      }
      readiness.assertNoErrors(page)
      await browser.close(); browser = undefined
    }
    // All advertised board dimensions are checked without regenerating every
    // difficulty: identities come from the already-passed native catalogue proof.
    for (const viewport of mobileViewports) {
      browser = await launch(engine)
      const context = await newProfile(browser, { ignoreHTTPSErrors: true, viewport, isMobile: true, hasTouch: true })
      await instrument(context)
      await context.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort())
      const page = await context.newPage(); lastPage = page; page.setDefaultTimeout(20000); readiness.watch(page, origin)
      for (const sample of cases) {
        receipt.current = { engine: engine.name, device: 'mobile', game: sample.game, geometry: sample.key, path: sample.path, width: viewport.width, height: viewport.height, phase: 'advertised geometry and readable fit/zoom' }
        await readiness.board(page, origin + sample.path)
        if (viewport.width===390 && largestCases.get(sample.game)===sample) {
          await page.emulateMedia({colorScheme:'light'})
          await settleViewport(page)
          await captureViewport(page,engine.name,'mobile',sample.game,'largest-light')
        }
        const metrics = await verifyPlayArea(page, true)
        const proof={ engine: engine.name, width: viewport.width, key: sample.key, path: sample.path, board: metrics.board, verifiedZoom: metrics.verifiedZoom === true }
        receipt.geometry.push(proof)
        if(sample.path===largeShikaku.path){receipt.current.phase='large Shikaku exact touch paint, no page pan, Undo/Redo/reload';const corners=await prepareShikakuTouch(page);proof.edit=await editAndRestore(page,'Shikaku',true,engine.name,readiness,corners)}
      }
      readiness.assertNoErrors(page)
      await browser.close(); browser = undefined; save()
    }
  }
  receipt.complete = true
} catch (error) {
  receipt.failure = error.stack
  receipt.failureContext = { ...receipt.current }
  if (error.browserDiagnostic) receipt.failureDiagnostic = error.browserDiagnostic
  if (lastPage && !lastPage.isClosed()) {
    try { receipt.failureContext.path = new URL(lastPage.url()).pathname } catch {}
    try { receipt.failureContext.board = (await playMetrics(lastPage)).board } catch {}
    await lastPage.screenshot({ path: output + '/failure.png', fullPage: false })
    receipt.failureText = (await lastPage.locator('body').innerText()).slice(-8000)
  }
  throw error
} finally {
  if (supportBrowser) await supportBrowser.close()
  if (browser) await browser.close()
  save()
}
console.log(JSON.stringify({ complete: true, contract: contractVersion, journeys: receipt.journeys.length, geometries: receipt.geometry.length }))
