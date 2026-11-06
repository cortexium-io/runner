// Synthetic adapter fixtures only: no product source, network service or models.
import assert from 'node:assert/strict'
import http from 'node:http'
import { pathToFileURL } from 'node:url'
import { verifyPlayArea, editAndRestore, dragRectangle, activate, prepareShikakuTouch, renderedState, assertRenderedState, observeAdmission } from './browser_contract.mjs'
import { PageReadiness, BrowserAdmissionError, BrowserResourceError, admissionDelay } from './browser_render.mjs'
const { chromium, webkit } = await import(pathToFileURL(process.env.RUNNER_PLAYWRIGHT_MODULE).href)

function semanticFixture(fault, rate = false) {
  const behavior = fault.split('_')[0].replace(/^rate-/, '')
  const admission = rate ? '/api/games/' + (behavior === 'unrelated' ? 'choices' : 'nonogram') : '/api/fixture-admission/' + fault + '?private-query=must-not-appear'
  return `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/style.css"><body><div class="player-board-frame" style="visibility:hidden"><div class="nonogram-board"><button data-cell="0"><span class="player-mark"></span></button><button data-cell="1"></button></div></div><div class="value-controls"><button id="shade">Shade</button><button id="undo">Undo</button><button id="redo">Redo</button></div><script>
const fault=${JSON.stringify(fault)},behavior=${JSON.stringify(behavior)},key='games-site:semantic:'+fault,board=document.querySelector('.nonogram-board'),cell=board.querySelector('[data-cell="0"]'),actions=JSON.parse(localStorage.getItem('fixture-actions:'+fault)||'{"shade":0,"undo":0,"redo":0}');
let state=JSON.parse(localStorage.getItem(key)||'null')?.snapshot||{cells:['blank','blank']},past=[],future=[];const restored=state.cells[0]==='filled';
if(restored&&behavior==='saved')state={cells:['filled','filled']};
function render(){cell.dataset.value=state.cells[0];cell.querySelector('.player-mark').textContent=state.cells[0]==='filled'?'■':'';localStorage.setItem(key,JSON.stringify({snapshot:state}));}
function action(name){actions[name]++;localStorage.setItem('fixture-actions:'+fault,JSON.stringify(actions))}
document.querySelector('#shade').onclick=()=>{action('shade');past.push(state);state={cells:['filled','blank']};future=[];render()};
document.querySelector('#undo').onclick=()=>{action('undo');if(past.length){future.unshift(state);state=past.pop();render()}};
document.querySelector('#redo').onclick=()=>{action('redo');if(future.length){past.push(state);state=future.shift();render()}};
fetch(${JSON.stringify(admission)},${rate ? "{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({fault,restored})}" : 'undefined'}).then(response=>{if(!response.ok)return;render();if(restored){const mark=cell.querySelector('.player-mark');
if(behavior==='decoration'){const warning=document.createElement('span');warning.className='wrong-mark';warning.textContent='!';cell.append(warning)}
if(behavior==='other-decoration'){const warning=document.createElement('span');warning.className='other-mark';warning.textContent='!';cell.append(warning)}
if(behavior==='value')cell.dataset.value='blank';
if(behavior==='hidden')mark.style.visibility='hidden';
if(behavior==='transparent')mark.style.opacity='0';
if(behavior==='missing')mark.remove();
if(behavior==='identity')cell.dataset.cell='9';
if(behavior==='long'){mark.textContent='x'.repeat(1000);mark.style.display='block';mark.style.overflow='hidden';}
}document.querySelector('.player-board-frame').style.visibility='visible'});
</script>`
}

const admissions = new Map(), documents = new Map()
const server = http.createServer((req, res) => {
  const path = new URL(req.url, 'http://fixture').pathname
  if (req.method === 'POST' && ['/api/games/nonogram', '/api/games/choices'].includes(path)) {
    let body = ''
    req.on('data', chunk => { body += chunk })
    req.on('end', () => {
      const { fault, restored } = JSON.parse(body), behavior = fault.split('_')[0].replace(/^rate-/, '')
      let status = 200
      if (restored) {
        const rows = admissions.get(fault) ?? []; rows.push(Date.now()); admissions.set(fault, rows)
        if (behavior === 'transport') { req.socket.destroy(); return }
        status = behavior === 'document' ? 200 : behavior === 'fivehundred' ? 500 : rows.length === 1 || behavior === 'persistent' ? 429 : 200
        if (status === 429 && behavior !== 'noheader') res.setHeader('Retry-After', behavior === 'invalid' ? 'not-a-delay' : behavior === 'oversized' ? '31' : behavior === 'budget' ? '30' : behavior === 'once' ? '1' : '0')
      }
      res.writeHead(status, { 'Content-Type': 'application/json' }); res.end('{"admitted":' + (status === 200) + '}')
    })
    return
  }
  if (path.startsWith('/nonogram/rate-')) {
    const fault = path.split('/').at(-1); documents.set(fault, (documents.get(fault) ?? 0) + 1)
    if (fault.startsWith('rate-document_') && documents.get(fault) === 2) { res.writeHead(429, { 'Retry-After': '0', 'Content-Type': 'text/html' }); return res.end('Rate limited document') }
    res.setHeader('Content-Type', 'text/html'); return res.end(semanticFixture(fault, true))
  }
  if (path.startsWith('/api/fixture-admission/')) { res.setHeader('Content-Type', 'application/json'); return res.end('{"admitted":true}') }
  if (path.startsWith('/semantic/')) { res.setHeader('Content-Type', 'text/html'); return res.end(semanticFixture(path.split('/').at(-1))) }
  if (req.url === '/style.css') {
    res.setHeader('Content-Type', 'text/css')
    return res.end('body{margin:0;padding:8px;box-sizing:border-box}button{font:14px sans-serif;height:40px;touch-action:manipulation}.player-board-frame{width:200px;margin-top:20px;overflow:hidden}.shikaku-board{display:grid;grid-template-columns:repeat(5,40px);width:200px;touch-action:none}.shikaku-board button{width:40px;padding:0;box-sizing:border-box;background:var(--rectangle-fill,#fff)}.nonogram-board{display:grid;grid-template-columns:repeat(2,40px);width:80px}.nonogram-board button{width:40px}body[data-case^=large] .player-board-frame{width:240px;height:240px}body[data-case^=large] .shikaku-board{width:240px;grid-template-columns:repeat(20,12px)}body[data-case^=large] .shikaku-board button{width:12px;height:12px;font-size:8px}.rectangle-controls{display:flex;flex-wrap:wrap}body[data-case=clip] .shikaku-board{width:420px;grid-template-columns:repeat(5,84px)}body[data-case=internal] .shikaku-board{grid-template-columns:repeat(5,84px)}body[data-case=entry] .player-board-frame{margin-top:900px}body[data-case=control] .rectangle-controls{margin-top:900px}body[data-case=zoom] .shikaku-board button{font-size:10px}')
  }
  const gridSize=req.url.startsWith('/large')?20:5
  res.setHeader('Content-Type', 'text/html')
  res.end(`<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/style.css"><body data-case="${req.url.slice(1)}" data-grid="${gridSize}"><div class="player-board-frame"><div class="shikaku-board">${Array.from({ length: gridSize*gridSize }, (_, i) => `<button data-cell="${i}">${i === 0 ? '4' : ''}</button>`).join('')}</div></div><div class="rectangle-controls"><button id="add">Add rectangle</button><button id="undo">Undo</button><button id="redo">Redo</button></div><button id="zoom">Zoom in</button><button id="fit">Fit board</button><script>
const n=${gridSize},key='games-site:fixture:'+n,board=document.querySelector('.shikaku-board');let state=JSON.parse(localStorage.getItem(key)||'null')?.snapshot||{rectangles:[]},past=[],future=[],corners=[],dragStart;
function render(){const r=state.rectangles[0];document.querySelectorAll('[data-cell]').forEach(node=>{const i=Number(node.dataset.cell),inside=r&&Math.floor(i/n)>=Math.floor(r.first/n)&&Math.floor(i/n)<=Math.floor(r.last/n)&&i%n>=r.first%n&&i%n<=r.last%n;node.style.setProperty('--rectangle-fill',inside?'#bbf':'')});localStorage.setItem(key,JSON.stringify({snapshot:state}));}
function commit(first,last){if(first!=='0'||last!==String(n+1))return;past.push(state);state={rectangles:[{first:0,last:document.body.dataset.case==='wrongpaint'||document.body.dataset.case==='large-bad'?0:n+1}]};future=[];render();}
board.addEventListener('pointerdown',e=>{if(document.body.dataset.case==='ignored')return;dragStart=e.target.dataset.cell;if(dragStart!==undefined)board.setPointerCapture(e.pointerId)});
board.addEventListener('pointerup',e=>{if(board.hasPointerCapture(e.pointerId))board.releasePointerCapture(e.pointerId);if(document.body.dataset.case==='ignored')return;const target=document.elementFromPoint(e.clientX,e.clientY)?.closest('[data-cell]');if(dragStart!==undefined&&target?.dataset.cell!==dragStart)commit(dragStart,target?.dataset.cell);dragStart=undefined;});
board.addEventListener('pointercancel',e=>{if(board.hasPointerCapture(e.pointerId))board.releasePointerCapture(e.pointerId);corners=[];dragStart=undefined});
board.addEventListener('click',e=>{if(document.body.dataset.case==='ignored')return;if(e.target.dataset.cell!==undefined)corners.push(e.target.dataset.cell)});
document.querySelector('#add').onclick=()=>{if(document.body.dataset.case!=='ignored'&&corners.length===2)commit(corners[0],corners[1])};
document.querySelector('#undo').onclick=()=>{if(past.length){future.unshift(state);state=past.pop();render()}};
document.querySelector('#redo').onclick=()=>{if(future.length){past.push(state);state=future.shift();render()}};
document.querySelector('#zoom').onclick=()=>{const cell=n===20?32:48;board.style.width=(n*cell)+'px';board.style.gridTemplateColumns='repeat('+n+','+cell+'px)';board.parentElement.style.width='240px';document.querySelectorAll('[data-cell]').forEach(node=>{node.style.fontSize='14px';node.style.width=cell+'px';node.style.height=cell+'px'})};
document.querySelector('#fit').onclick=()=>{const cell=n===20?12:40;board.style.width=(n*cell)+'px';board.style.gridTemplateColumns='repeat('+n+','+cell+'px)';board.parentElement.style.width=(n*cell)+'px';document.querySelectorAll('[data-cell]').forEach(node=>{node.style.fontSize=(n===20?8:10)+'px';node.style.width=cell+'px';node.style.height=cell+'px'})};
render();</script>`)
})
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
const origin = 'http://127.0.0.1:' + server.address().port
assert.deepEqual(admissionDelay(null), { retryAfterMs: null, delayMs: 1000 })
assert.deepEqual(admissionDelay('0'), { retryAfterMs: 0, delayMs: 0 })
assert.deepEqual(admissionDelay('30'), { retryAfterMs: 30000, delayMs: 30000 })
const clock = Date.UTC(2026, 10, 7)
assert.deepEqual(admissionDelay(new Date(clock + 1000).toUTCString(), clock), { retryAfterMs: 1000, delayMs: 1000 })
for (const value of ['', '-1', '0.1', '31', '1000', 'not-a-delay', 'x'.repeat(129), new Date(clock + 31000).toUTCString()]) assert.equal(admissionDelay(value, clock), null, 'unbounded/invalid delay accepted')
try {
  for (const engine of [{ name: 'chromium', type: chromium, path: process.env.RUNNER_BROWSER_EXECUTABLE }, { name: 'webkit', type: webkit, path: process.env.RUNNER_WEBKIT_EXECUTABLE }]) {
    assert.ok(engine.path, 'missing configured ' + engine.name + ' prerequisite')
    const launch = () => engine.type.launch({ executablePath: engine.path, headless: true, args: engine.name === 'chromium' && process.platform === 'darwin' ? ['--disable-gpu', '--single-process', '--no-zygote'] : [] })
    let browser
    try {
      for (const width of [320, 360, 390]) {
        browser = await launch()
        const context = await browser.newContext({ viewport: { width, height: 568 }, isMobile: true, hasTouch: true })
        await context.addInitScript(() => {
          window.runnerInputEvidence = { touch: 0, keyboard: 0 }
          window.runnerFixtureEvents = []
          for (const type of ['pointerdown','pointermove','pointerup','gotpointercapture','lostpointercapture','touchstart','touchend','click']) document.addEventListener(type,e=>{window.runnerFixtureEvents.push({type,pointer:e.pointerType,pointerId:e.pointerId,target:e.target.tagName,id:e.target.id,x:e.clientX,y:e.clientY,cell:e.target.dataset?.cell});window.runnerFixtureEvents=window.runnerFixtureEvents.slice(-25)},true)
          document.addEventListener('touchstart', e => { if (e.isTrusted) window.runnerInputEvidence.touch++ }, true)
          document.addEventListener('keydown', e => { if (e.isTrusted) window.runnerInputEvidence.keyboard++ }, true)
        })
        const page = await context.newPage(); page.setDefaultTimeout(1500)
        const ready = new PageReadiness({}); ready.watch(page, origin)
        for (const fault of ['clip', 'entry', 'control', 'internal']) {
          await ready.goto(page, origin + '/' + fault)
          await assert.rejects(verifyPlayArea(page, true), assert.AssertionError, 'known broken layout passed: ' + fault)
        }
        await ready.goto(page, origin + '/zoom')
        assert.equal((await verifyPlayArea(page, true)).verifiedZoom, true, 'intentional fit/zoom did not prove readable clues')
        await ready.goto(page, origin + '/valid')
        await verifyPlayArea(page, true)
        try { await editAndRestore(page, 'Shikaku', true, engine.name, ready) } catch(error) {
          console.error(JSON.stringify({engine:engine.name,width,evidence:await page.evaluate(()=>({input:window.runnerInputEvidence,events:window.runnerFixtureEvents,state:localStorage.getItem('games-site:fixture:'+document.body.dataset.grid)}))}))
          throw error
        }
        assert.deepEqual(await page.evaluate(()=>JSON.parse(localStorage.getItem('games-site:fixture:'+document.body.dataset.grid)).snapshot.rectangles),[{first:0,last:6}],'native interaction did not commit the intended start/end cells')
        await ready.goto(page, origin + '/large')
        await verifyPlayArea(page,true)
        const corners=await prepareShikakuTouch(page)
        await editAndRestore(page,'Shikaku',true,engine.name,ready,corners)
        assert.deepEqual(await page.evaluate(()=>JSON.parse(localStorage.getItem('games-site:fixture:'+document.body.dataset.grid)).snapshot.rectangles),[{first:0,last:21}],'large fitted board painted the wrong rectangle')
        await browser.close(); browser = undefined
      }
      browser = await launch()
      const context = await browser.newContext({ viewport: { width: 320, height: 568 }, isMobile: true, hasTouch: true })
      await context.addInitScript(() => { window.runnerInputEvidence = { touch: 0, keyboard: 0 }; document.addEventListener('touchstart', e => { if (e.isTrusted) window.runnerInputEvidence.touch++ }) })
      const page = await context.newPage(); page.setDefaultTimeout(700)
      const ready = new PageReadiness({}); ready.watch(page, origin)
      await ready.goto(page, origin + '/valid')
      await dragRectangle(page,true,engine.name,0,7)
      assert.deepEqual(await page.evaluate(()=>JSON.parse(localStorage.getItem('games-site:fixture:'+document.body.dataset.grid)).snapshot.rectangles),[],'wrong gesture endpoint produced a passing fixture edit')
      await ready.goto(page, origin + '/valid')
      await activate(page,page.locator('[data-cell="0"]'),true)
      await activate(page,page.getByRole('button',{name:'Add rectangle',exact:true}),true)
      assert.deepEqual(await page.evaluate(()=>JSON.parse(localStorage.getItem('games-site:fixture:'+document.body.dataset.grid)).snapshot.rectangles),[],'single-corner Add produced a passing fixture edit')
      await ready.goto(page, origin + '/wrongpaint')
      await assert.rejects(editAndRestore(page,'Shikaku',true,engine.name,ready),error=>error instanceof assert.AssertionError&&error.message.includes('painted cells differ'),'wrong committed rectangle passed exact visible paint proof')
      await ready.goto(page, origin + '/large-bad')
      const badCorners=await prepareShikakuTouch(page)
      await assert.rejects(editAndRestore(page,'Shikaku',true,engine.name,ready,badCorners),error=>error instanceof assert.AssertionError&&error.message.includes('painted cells differ'),'old pixel hit-testing passed fitted large-board proof')
      await ready.goto(page, origin + '/ignored')
      await assert.rejects(editAndRestore(page, 'Shikaku', true, engine.name, ready), error => error instanceof assert.AssertionError && error.message.includes('expected saved state'), 'ignored native touch was accepted')
      for (const fault of ['decoration', 'value', 'hidden', 'transparent', 'missing', 'identity', 'other-decoration', 'saved', 'long']) {
        await ready.goto(page, origin + '/semantic/' + fault)
        await page.locator('.player-board-frame:visible').waitFor({ state: 'visible' })
        if (fault === 'decoration') {
          await editAndRestore(page, 'Nonogram', true, engine.name, ready)
          assert.equal(await page.locator('.wrong-mark').textContent(), '!', 'fixture did not restore transient check decoration')
          assert.equal((await renderedState(page)).cells[0].text, '■', 'primary player mark was discarded with warning decoration')
          continue
        }
        await assert.rejects(editAndRestore(page, 'Nonogram', true, engine.name, ready), error => {
          assert.ok(error instanceof assert.AssertionError, 'semantic source failure lost assertion classification')
          const diagnostic = error.browserDiagnostic
          assert.ok(Buffer.byteLength(JSON.stringify(diagnostic)) < 4096, 'private semantic diagnostic exceeded its fixture bound')
          assert.equal(diagnostic.phase, 'reload')
          assert.equal(diagnostic.path, '/semantic/' + fault)
          assert.equal(diagnostic.reload.status, 'complete')
          assert.equal(diagnostic.reload.documentStatus, 200)
          assert.ok(diagnostic.reload.responses.some(row => row.method === 'GET' && row.path === '/api/fixture-admission/' + fault && row.status === 200))
          assert.ok(!JSON.stringify(diagnostic).includes('private-query'), 'reload diagnostics exposed query data')
          if (fault === 'saved') {
            assert.equal(diagnostic.kind, 'saved-state')
            assert.notEqual(diagnostic.expected.sha256, diagnostic.actual.sha256)
            assert.equal(diagnostic.expected.keys, diagnostic.actual.keys)
            assert.ok(!JSON.stringify(diagnostic).includes('snapshot'), 'saved-state diagnostic exposed storage contents')
          } else {
            assert.equal(diagnostic.kind, 'visible-state', 'wrong diagnostic for ' + fault)
            assert.equal(diagnostic.cell, '0')
            assert.equal(diagnostic.selector, '.nonogram-board [data-cell="0"]')
            assert.equal(diagnostic.expected.identity, 'nonogram-board:data-cell:0')
            assert.equal(diagnostic.expected.text, '■')
            assert.equal(diagnostic.expected.value, 'filled')
            assert.equal(diagnostic.status, fault === 'long' ? 'truncated' : 'complete')
            if (fault === 'long') assert.equal(diagnostic.actual.text.length, 256)
          }
          return true
        }, 'broken restored primary state passed: ' + fault)
      }
      // The same semantic predicate is used for home resume, with its own phase.
      const expected = await renderedState(page)
      await page.locator('[data-cell="0"]').evaluate(node => node.remove())
      const missingCell = await renderedState(page)
      assert.throws(() => assertRenderedState(missingCell, expected, 'resume primary cell missing', { phase: 'home-resume', path: '/semantic/long' }), error => error.browserDiagnostic.phase === 'home-resume' && error.browserDiagnostic.actual.identity !== error.browserDiagnostic.expected.identity)
      // Transport failures remain infrastructure failures with bounded facts.
      const failure = new Error('synthetic navigation interruption')
      await assert.rejects(observeAdmission(page, 'reload', async () => { throw failure }), error => error === failure && error.browserDiagnostic.reload.status === 'missing' && !(error instanceof assert.AssertionError))
      const burstFailure = new Error('synthetic admission burst')
      await assert.rejects(observeAdmission(page, 'reload', async () => {
        await page.evaluate(async () => { await Promise.all(Array.from({ length: 10 }, (_, i) => fetch('/api/fixture-admission/burst?private-query=' + i))) })
        throw burstFailure
      }), error => error === burstFailure && error.browserDiagnostic.reload.status === 'truncated' && error.browserDiagnostic.reload.responses.length === 8 && !JSON.stringify(error.browserDiagnostic).includes('private-query'))
      for (const behavior of ['once', 'noheader', 'document', 'persistent', 'invalid', 'oversized', 'budget', 'unknown', 'fivehundred', 'transport', 'unrelated', 'saved', 'value', 'identity']) {
        assert.notEqual(behavior, 'missing', 'rate-header cases must not select the semantic missing-mark mutation')
        const fault = `rate-${behavior}_${engine.name}`, canonical = origin + '/nonogram/' + fault, evidence = {}
        const navigation = new PageReadiness(evidence, behavior === 'unknown' ? null : Date.now() + (behavior === 'budget' ? 5000 : 60000))
        navigation.watch(page, origin)
        await navigation.goto(page, canonical)
        await page.locator('.player-board-frame:visible').waitFor({ state: 'visible' })
        const run = () => editAndRestore(page, 'Nonogram', true, engine.name, navigation)
        if (['once', 'noheader', 'document'].includes(behavior)) {
          await run()
          assert.deepEqual(await page.evaluate(fault => JSON.parse(localStorage.getItem('games-site:semantic:' + fault)).snapshot, fault), { cells: ['filled', 'blank'] }, 'backoff changed saved board state')
          assert.equal((await renderedState(page)).cells[0].value, 'filled')
          if (behavior === 'noheader') assert.equal((await renderedState(page)).cells[0].text, '■', 'missing Retry-After must preserve the primary mark')
        } else if (['saved', 'value', 'identity'].includes(behavior)) {
          await assert.rejects(run(), error => error instanceof assert.AssertionError && error.browserDiagnostic.kind === (behavior === 'saved' ? 'saved-state' : 'visible-state'), 'backoff waived restored state/identity mismatch')
        } else {
          await assert.rejects(run(), error => {
            assert.ok(!(error instanceof assert.AssertionError), 'infrastructure admission failure became a product assertion')
            if (behavior !== 'unrelated') {
              assert.ok(error instanceof BrowserAdmissionError, 'admission failure lost typed infrastructure classification')
              const response = error.browserDiagnostic.reload.responses.findLast(row => row.path === '/api/games/nonogram')
              assert.equal(response.status, behavior === 'fivehundred' ? 500 : behavior === 'transport' ? null : 429)
              assert.ok(!JSON.stringify(error.browserDiagnostic).includes('not-a-delay'), 'raw delay header escaped into evidence')
            }
            return true
          }, 'unsupported admission was retried/passed: ' + behavior)
        }
        const retries = ['once', 'noheader', 'document', 'persistent', 'saved', 'value', 'identity'].includes(behavior) ? 1 : 0
        assert.equal(documents.get(fault), 2 + retries, 'canonical navigation was repeated unexpectedly: ' + behavior)
        assert.equal(evidence.admissionBackoffs?.length ?? 0, retries, 'backoff accounting differs: ' + behavior)
        assert.deepEqual(await page.evaluate(fault => JSON.parse(localStorage.getItem('fixture-actions:' + fault)), fault), { shade: 1, undo: 1, redo: 1 }, 'edit/Undo/Redo replayed during navigation retry')
        if (['once', 'noheader'].includes(behavior)) {
          const times = admissions.get(fault)
          assert.ok(times[1] - times[0] >= 1000, 'retry ignored its server/default delay')
        }
      }
      // An enter action is called once; the retry is a literal canonical GET.
      const fault = `rate-enter_${engine.name}`, canonical = origin + '/nonogram/' + fault
      await ready.goto(page, canonical)
      await page.locator('.player-board-frame:visible').waitFor({ state: 'visible' })
      await activate(page, page.getByRole('button', { name: 'Shade', exact: true }), true)
      let enters = 0
      const navigation = new PageReadiness({}, Date.now() + 60000); navigation.watch(page, origin)
      await navigation.board(page, canonical, async () => { enters++; await page.goto(canonical, { waitUntil: 'load' }) })
      assert.equal(enters, 1, 'saved-entry action replayed instead of canonical GET')
      assert.equal(documents.get(fault), 3)
      for (const persistent of [false, true]) {
        const resourceFault = `rate-resource${persistent ? 'persistent' : 'once'}_${engine.name}`, resourceEvidence = {}, resources = new PageReadiness(resourceEvidence, Date.now() + 60000)
        resources.watch(page, origin)
        let styles = 0, resourceEnters = 0
        await page.route('**/style.css', route => ++styles === 1 || persistent ? route.abort('failed') : route.continue())
        const open = () => resources.board(page, origin + '/nonogram/' + resourceFault, async () => {
          resourceEnters++; await page.goto(origin + '/nonogram/' + resourceFault, { waitUntil: 'load' })
        })
        try {
          if (persistent) await assert.rejects(open(), BrowserResourceError, 'persistent critical-resource failure passed')
          else await open()
        } finally { await page.unroute('**/style.css') }
        assert.equal(resourceEnters, 1, 'critical-resource recovery replayed the saved-entry callback')
        assert.equal(documents.get(resourceFault), 2, 'critical-resource recovery repeated more than one canonical GET')
        assert.equal(resourceEvidence.resourceRetries.length, 1)
        assert.equal(resourceEvidence.admissionBackoffs, undefined)
      }
      const combinedFault = `rate-combined_${engine.name}`, combinedPath = origin + '/nonogram/' + combinedFault, combinedEvidence = {}, combined = new PageReadiness(combinedEvidence, Date.now() + 60000)
      combined.watch(page, origin)
      await combined.goto(page, combinedPath)
      await page.locator('.player-board-frame:visible').waitFor({ state: 'visible' })
      let styles = 0
      await page.route('**/style.css', route => ++styles >= 2 ? route.abort('failed') : route.continue())
      try { await assert.rejects(editAndRestore(page, 'Nonogram', true, engine.name, combined), BrowserResourceError, 'rate retry reset the critical-resource budget') }
      finally { await page.unroute('**/style.css') }
      assert.equal(documents.get(combinedFault), 4)
      assert.equal(combinedEvidence.resourceRetries.length, 1)
      assert.equal(combinedEvidence.admissionBackoffs.length, 1)
      const collisionFault = `rate-fivehundred_${engine.name}collision`, collisionPath = origin + '/nonogram/' + collisionFault, collisionEvidence = {}, collision = new PageReadiness(collisionEvidence, Date.now() + 60000)
      collision.watch(page, origin)
      await ready.goto(page, collisionPath)
      await page.locator('.player-board-frame:visible').waitFor({ state: 'visible' })
      await activate(page, page.getByRole('button', { name: 'Shade', exact: true }), true)
      await page.route('**/style.css', route => route.abort('failed'))
      let collisionEnters = 0
      try {
        await assert.rejects(collision.board(page, collisionPath, async () => {
          collisionEnters++
          const denied = page.waitForResponse(row => new URL(row.url()).pathname === '/api/games/nonogram' && row.status() === 500)
          await page.goto(collisionPath, { waitUntil: 'load' }); await denied
        }), BrowserResourceError, 'resource retry replayed a known failed API admission')
      } finally { await page.unroute('**/style.css') }
      assert.equal(collisionEnters, 1)
      assert.equal(documents.get(collisionFault), 2, 'known API500 was replayed because CSS also failed')
      assert.equal(collisionEvidence.resourceRetries, undefined)
      assert.equal(collisionEvidence.admissionBackoffs, undefined)
      const assertionFault = `rate-assert_${engine.name}`, assertionPath = origin + '/nonogram/' + assertionFault
      await ready.goto(page, assertionPath)
      await page.locator('.player-board-frame:visible').waitFor({ state: 'visible' })
      await activate(page, page.getByRole('button', { name: 'Shade', exact: true }), true)
      const assertion = new assert.AssertionError({ message: 'caller assertion must never replay' }), assertions = new PageReadiness({}, Date.now() + 60000)
      assertions.watch(page, origin)
      await assert.rejects(assertions.board(page, assertionPath, async () => {
        const response = page.waitForResponse(row => new URL(row.url()).pathname === '/api/games/nonogram' && row.status() === 429)
        await page.goto(assertionPath, { waitUntil: 'load' }); await response
        throw assertion
      }), error => error === assertion)
      assert.equal(documents.get(assertionFault), 2, 'an assertion was replaced by an admission retry')
      assert.equal(assertions.receipt.admissionBackoffs, undefined)
    } finally { if (browser) await browser.close() }
  }
  console.log('PASS: both engines; real touch edit/Undo/Redo/reload; 320/360/390; strict semantic restore; one bounded canonical 429 backoff without replayed actions/assertions; rejected persistent/invalid/error admissions and changed values/identities/snapshots')
} finally { await new Promise(resolve => server.close(resolve)) }
