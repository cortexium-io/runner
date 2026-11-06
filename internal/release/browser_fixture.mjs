// Synthetic adapter fixtures only: no product source, network service or models.
import assert from 'node:assert/strict'
import http from 'node:http'
import { pathToFileURL } from 'node:url'
import { verifyPlayArea, editAndRestore, dragRectangle, activate, prepareShikakuTouch, renderedState, assertRenderedState, observeAdmission } from './browser_contract.mjs'
import { PageReadiness } from './browser_render.mjs'
const { chromium, webkit } = await import(pathToFileURL(process.env.RUNNER_PLAYWRIGHT_MODULE).href)

function semanticFixture(fault) {
  return `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/style.css"><body><div class="player-board-frame" style="visibility:hidden"><div class="nonogram-board"><button data-cell="0"><span class="player-mark"></span></button><button data-cell="1"></button></div></div><div class="value-controls"><button id="shade">Shade</button><button id="undo">Undo</button><button id="redo">Redo</button></div><script>
const fault=${JSON.stringify(fault)},key='games-site:semantic:'+fault,board=document.querySelector('.nonogram-board'),cell=board.querySelector('[data-cell="0"]');
let state=JSON.parse(localStorage.getItem(key)||'null')?.snapshot||{cells:['blank','blank']},past=[],future=[];const restored=state.cells[0]==='filled';
if(restored&&fault==='saved')state={cells:['filled','filled']};
function render(){cell.dataset.value=state.cells[0];cell.querySelector('.player-mark').textContent=state.cells[0]==='filled'?'■':'';localStorage.setItem(key,JSON.stringify({snapshot:state}));}
document.querySelector('#shade').onclick=()=>{past.push(state);state={cells:['filled','blank']};future=[];render()};
document.querySelector('#undo').onclick=()=>{if(past.length){future.unshift(state);state=past.pop();render()}};
document.querySelector('#redo').onclick=()=>{if(future.length){past.push(state);state=future.shift();render()}};
fetch('/api/fixture-admission/'+fault+'?private-query=must-not-appear').then(response=>{if(!response.ok)throw Error('fixture admission');render();if(restored){const mark=cell.querySelector('.player-mark');
if(fault==='decoration'){const warning=document.createElement('span');warning.className='wrong-mark';warning.textContent='!';cell.append(warning)}
if(fault==='other-decoration'){const warning=document.createElement('span');warning.className='other-mark';warning.textContent='!';cell.append(warning)}
if(fault==='value')cell.dataset.value='blank';
if(fault==='hidden')mark.style.visibility='hidden';
if(fault==='transparent')mark.style.opacity='0';
if(fault==='missing')mark.remove();
if(fault==='identity')cell.dataset.cell='9';
if(fault==='long'){mark.textContent='x'.repeat(1000);mark.style.display='block';mark.style.overflow='hidden';}
}document.querySelector('.player-board-frame').style.visibility='visible'});
</script>`
}

const server = http.createServer((req, res) => {
  const path = new URL(req.url, 'http://fixture').pathname
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
    } finally { if (browser) await browser.close() }
  }
  console.log('PASS: both engines; real touch edit/Undo/Redo/reload; 320/360/390; strict semantic restore ignores only wrong-mark decoration; rejected primary value/visibility/identity/snapshot changes, clipping, entry scroll, unreachable actions and ignored touch')
} finally { await new Promise(resolve => server.close(resolve)) }
