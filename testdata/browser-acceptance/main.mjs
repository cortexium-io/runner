// Synthetic adapter fixtures only: no product source, network service or models.
import assert from 'node:assert/strict'
import http from 'node:http'
import { pathToFileURL } from 'node:url'
import { verifyPlayArea, editAndRestore } from './browser_contract.mjs'
import { PageReadiness } from './browser_render.mjs'
const { chromium, webkit } = await import(pathToFileURL(process.env.RUNNER_PLAYWRIGHT_MODULE).href)
const server = http.createServer((req, res) => {
  if (req.url === '/style.css') {
    res.setHeader('Content-Type', 'text/css')
    return res.end('body{margin:0;padding:8px;box-sizing:border-box}button{font:14px sans-serif;height:40px}.player-board-frame{width:200px;margin-top:20px;overflow:hidden}.shikaku-board{display:grid;grid-template-columns:repeat(5,40px);width:200px;touch-action:none}.shikaku-board button{width:40px;padding:0;box-sizing:border-box}.rectangle-controls{display:flex;flex-wrap:wrap}body[data-case=clip] .shikaku-board{width:420px;grid-template-columns:repeat(5,84px)}body[data-case=entry] .player-board-frame{margin-top:900px}body[data-case=control] .rectangle-controls{margin-top:900px}body[data-case=zoom] .shikaku-board button{font-size:10px}')
  }
  res.setHeader('Content-Type', 'text/html')
  res.end(`<!doctype html><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/style.css"><body data-case="${req.url.slice(1)}"><div class="player-board-frame"><div class="shikaku-board">${Array.from({ length: 25 }, (_, i) => `<button data-cell="${i}">${i === 0 ? '4' : ''}</button>`).join('')}</div></div><div class="rectangle-controls"><button id="add">Add rectangle</button><button id="undo">Undo</button><button id="redo">Redo</button></div><button id="zoom">Zoom in</button><button id="fit">Fit board</button><script>
const key='games-site:fixture',board=document.querySelector('.shikaku-board');let state=JSON.parse(localStorage.getItem(key)||'null')?.snapshot||{rectangles:[]},past=[],future=[],corners=[];
function render(){document.querySelectorAll('[data-cell]').forEach(n=>n.style.setProperty('--rectangle-fill',state.rectangles.length?'#bbf':''));localStorage.setItem(key,JSON.stringify({snapshot:state}));}
function commit(){past.push(state);state={rectangles:[{first:0,last:6}]};future=[];render();}
board.addEventListener('pointerdown',e=>{if(document.body.dataset.case==='ignored')return;corners=[e.target.dataset.cell];if(e.target.dataset.cell!==undefined)board.setPointerCapture(e.pointerId)});
board.addEventListener('pointerup',e=>{if(document.body.dataset.case==='ignored')return;const target=document.elementFromPoint(e.clientX,e.clientY);if(corners[0]!==undefined&&target?.dataset.cell!==corners[0])commit();});
board.addEventListener('click',e=>{if(document.body.dataset.case==='ignored')return;if(e.target.dataset.cell!==undefined)corners.push(e.target.dataset.cell)});
document.querySelector('#add').onclick=()=>{if(document.body.dataset.case!=='ignored'&&corners.length)commit()};
document.querySelector('#undo').onclick=()=>{if(past.length){future.unshift(state);state=past.pop();render()}};
document.querySelector('#redo').onclick=()=>{if(future.length){past.push(state);state=future.shift();render()}};
document.querySelector('#zoom').onclick=()=>{board.style.width='240px';board.style.gridTemplateColumns='repeat(5,48px)';board.parentElement.style.width='240px';document.querySelectorAll('[data-cell]').forEach(n=>{n.style.fontSize='14px';n.style.width='48px'})};
document.querySelector('#fit').onclick=()=>{board.style.width='200px';board.style.gridTemplateColumns='repeat(5,40px)';board.parentElement.style.width='200px';document.querySelectorAll('[data-cell]').forEach(n=>{n.style.fontSize='10px';n.style.width='40px'})};
render();</script>`)
})
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
const origin = 'http://127.0.0.1:' + server.address().port
try {
  for (const engine of [{ name: 'chromium', type: chromium, path: process.env.RUNNER_BROWSER_EXECUTABLE }, { name: 'webkit', type: webkit, path: process.env.RUNNER_WEBKIT_EXECUTABLE }]) {
    assert.ok(engine.path, 'missing configured ' + engine.name + ' prerequisite')
    const browser = await engine.type.launch({ executablePath: engine.path, headless: true, args: engine.name === 'chromium' ? ['--disable-gpu', '--single-process', '--no-zygote'] : [] })
    try {
      for (const width of [320, 360, 390]) {
        const context = await browser.newContext({ viewport: { width, height: 568 }, isMobile: true, hasTouch: true })
        await context.addInitScript(() => {
          window.runnerInputEvidence = { touch: 0, keyboard: 0 }
          document.addEventListener('touchstart', e => { if (e.isTrusted) window.runnerInputEvidence.touch++ }, true)
          document.addEventListener('keydown', e => { if (e.isTrusted) window.runnerInputEvidence.keyboard++ }, true)
        })
        const page = await context.newPage(); page.setDefaultTimeout(1500)
        const ready = new PageReadiness({}); ready.watch(page, origin)
        for (const fault of ['clip', 'entry', 'control']) {
          await ready.goto(page, origin + '/' + fault)
          await assert.rejects(verifyPlayArea(page, true), assert.AssertionError, 'known broken layout passed: ' + fault)
        }
        await ready.goto(page, origin + '/zoom')
        assert.equal((await verifyPlayArea(page, true)).verifiedZoom, true, 'intentional fit/zoom did not prove readable clues')
        await ready.goto(page, origin + '/valid')
        await verifyPlayArea(page, true)
        await editAndRestore(page, 'Shikaku', true, engine.name, ready)
        await context.close()
      }
      const context = await browser.newContext({ viewport: { width: 320, height: 568 }, isMobile: true, hasTouch: true })
      await context.addInitScript(() => { window.runnerInputEvidence = { touch: 0, keyboard: 0 }; document.addEventListener('touchstart', e => { if (e.isTrusted) window.runnerInputEvidence.touch++ }) })
      const page = await context.newPage(); page.setDefaultTimeout(700)
      const ready = new PageReadiness({}); ready.watch(page, origin)
      await ready.goto(page, origin + '/ignored')
      await assert.rejects(editAndRestore(page, 'Shikaku', true, engine.name, ready), error => error instanceof assert.AssertionError && error.message.includes('expected saved state'), 'ignored native touch was accepted')
      await context.close()
    } finally { await browser.close() }
  }
  console.log('PASS: both engines; real touch edit/Undo/Redo/reload; 320/360/390; rejected clipping, entry scroll, unreachable actions and ignored touch')
} finally { await new Promise(resolve => server.close(resolve)) }
