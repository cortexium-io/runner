// Synthetic adapter fixtures only: no product source, network service or models.
import assert from 'node:assert/strict'
import http from 'node:http'
import { pathToFileURL } from 'node:url'
import { verifyPlayArea, editAndRestore, dragRectangle, activate, prepareShikakuTouch } from './browser_contract.mjs'
import { PageReadiness } from './browser_render.mjs'
const { chromium, webkit } = await import(pathToFileURL(process.env.RUNNER_PLAYWRIGHT_MODULE).href)
const server = http.createServer((req, res) => {
  if (req.url === '/style.css') {
    res.setHeader('Content-Type', 'text/css')
    return res.end('body{margin:0;padding:8px;box-sizing:border-box}button{font:14px sans-serif;height:40px;touch-action:manipulation}.player-board-frame{width:200px;margin-top:20px;overflow:hidden}.shikaku-board{display:grid;grid-template-columns:repeat(5,40px);width:200px;touch-action:none}.shikaku-board button{width:40px;padding:0;box-sizing:border-box;background:var(--rectangle-fill,#fff)}body[data-case^=large] .player-board-frame{width:240px;height:240px}body[data-case^=large] .shikaku-board{width:240px;grid-template-columns:repeat(20,12px)}body[data-case^=large] .shikaku-board button{width:12px;height:12px;font-size:8px}.rectangle-controls{display:flex;flex-wrap:wrap}body[data-case=clip] .shikaku-board{width:420px;grid-template-columns:repeat(5,84px)}body[data-case=internal] .shikaku-board{grid-template-columns:repeat(5,84px)}body[data-case=entry] .player-board-frame{margin-top:900px}body[data-case=control] .rectangle-controls{margin-top:900px}body[data-case=zoom] .shikaku-board button{font-size:10px}')
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
    } finally { if (browser) await browser.close() }
  }
  console.log('PASS: both engines; real touch edit/Undo/Redo/reload; 320/360/390; rejected clipping, entry scroll, unreachable actions and ignored touch')
} finally { await new Promise(resolve => server.close(resolve)) }
