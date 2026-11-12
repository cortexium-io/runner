import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import { activate, playMetrics, savedState, assertSavedState, renderedState, assertRenderedState } from './browser_contract.mjs'

// Fixed observations only. Neither a candidate nor task data selects scripts,
// URLs, filenames or commands. These images describe actual viewport states;
// they do not replace the independent image judgment or interaction obligations.
export async function captureViewport(receipt, output, page, engine, device, game, state = '') {
  const name = `${engine}-${device}-${game.toLowerCase()}${state ? '-' + state : ''}-viewport.png`
  assert.match(name,/^(chromium|webkit)-(desktop|mobile)-(sudoku|suguru|nonogram|binary|futoshiki|shikaku|hashi|calcudoku|kakuro)(-(chooser-dark|initial|largest-light|landscape|tablet-phone|hint|preferences|notes|commit|privacy|hint-conflict|solved|pan-context|chooser-light|chooser-landscape))?-viewport\.png$/)
  assert.ok(receipt.viewportImages.length < 160 && !receipt.viewportImages.some(row=>row.name===name), 'viewport evidence exceeds its fixed matrix')
  const png = await page.screenshot({path:`${output}/${name}`,fullPage:false,scale:'css'})
  assert.ok(png.length<=8*1024*1024,'viewport image exceeds allowance')
  receipt.viewportImages.push({name,sha256:crypto.createHash('sha256').update(png).digest('hex'),width:png.readUInt32BE(16),height:png.readUInt32BE(20)})
  const observed=await page.evaluate(()=>({viewport:[innerWidth,innerHeight],visualScale:visualViewport?.scale,scroll:[scrollX,scrollY],documentWidth:document.documentElement.scrollWidth}))
  assert.deepEqual([png.readUInt32BE(16),png.readUInt32BE(20)],observed.viewport,'image is not an actual CSS viewport crop')
  // Index binds to viewportImages without repeating its long filename/state or
  // pixel dimensions, keeping the full matrix in the unchanged receipt bound.
  const route=new URL(page.url()).pathname
  const known=receipt.journeys?.some(row=>row.engine===engine&&row.device===device&&row.game.toLowerCase()===game.toLowerCase()&&row.path===route)
  // Fixed data columns, not an executable recipe. Avoid four repeated keys per
  // frame so interaction witnesses fit the existing receipt ceiling.
  receipt.experienceViewFields=['scale','scrollX','scrollY','documentWidth']
  // Array position is the viewportImages index; both are appended together.
  receipt.experienceObservations.push({view:[observed.visualScale,...observed.scroll,observed.documentWidth],...(!known?{path:route}:{})})
  return name
}

export const settleViewport = page => page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))))

export class BrowserCapabilityError extends Error { name = 'BrowserCapabilityError' }
const requireProof = (condition, message) => { if (!condition) throw new BrowserCapabilityError(message) }
const scenarioCodes={engine:['chromium','webkit'],game:['sudoku','suguru','nonogram','binary','futoshiki','shikaku','hashi','calcudoku','kakuro'],case:['entry-preferences','contact-notes','F15','fresh-commits','privacy','representative-solved','F05/F06/F12/F14','F07/F08']}
export function recordScenario(receipt,engine,game,name,proof){
  const codes=[scenarioCodes.engine.indexOf(engine),scenarioCodes.game.indexOf(game.toLowerCase()),scenarioCodes.case.indexOf(name)]
  assert.ok(codes.every(n=>n>=0),'unknown fixed scenario code')
  receipt.experienceScenarioCodes=scenarioCodes
  receipt.experienceScenarioFields=['engine','game','case','proof']
  receipt.experienceScenarios.push([...codes,{status:'passed',...proof}])
  requireProof(Buffer.byteLength(JSON.stringify(receipt.experienceScenarios))<=12*1024,'fixed scenario witnesses exceed 12 KiB within the receipt bound')
}
const digest = value => crypto.createHash('sha256').update(JSON.stringify(value)).digest('hex')
const cells = page => page.locator('.player-board-frame button[data-cell]:not(.given):not([data-given="true"])')
const digit = (page, n) => page.locator('.number-pad button:visible,.value-controls button:visible').filter({hasText:new RegExp(`^\\s*${n}\\s*$`)}).first()
const notesButton = page => page.getByRole('button',{name:/^Notes(?:\s|$)/}).first()
const checks = page => page.locator('.play-settings input[type="checkbox"]').evaluateAll(nodes=>nodes.map(node=>({label:node.closest('label').textContent.trim().slice(0,80),checked:node.checked})))

// Fixed, passive observation. Synthetic DOM dispatch never contributes to proof.
async function traceInput(page, action, touch = true) {
  await page.evaluate(touch=>{
    const active=new Set(), proof={downs:0,ups:0,cancelled:0,maxContacts:0,clicks:0,keys:0}
    const observe=e=>{
      if(!e.isTrusted)return
      if(e.type==='keydown')proof.keys++
      if(e.type==='click')proof.clicks++
      if(e.pointerType!==(touch?'touch':'mouse'))return
      if(e.type==='pointerdown'){active.add(e.pointerId);proof.downs++;proof.maxContacts=Math.max(proof.maxContacts,active.size)}
      if(e.type==='pointerup'){active.delete(e.pointerId);proof.ups++}
      if(e.type==='pointercancel'){active.delete(e.pointerId);proof.cancelled++}
    }
    const types=['pointerdown','pointerup','pointercancel','click','keydown']
    for(const t of types)document.addEventListener(t,observe,{capture:true,passive:true})
    window.runnerScenarioInput={proof,stop(){for(const t of types)document.removeEventListener(t,observe,true);proof.remaining=active.size}}
  },touch)
  let proof
  try { await action() } finally {
    proof=await page.evaluate(()=>{const p=window.runnerScenarioInput;p.stop();delete window.runnerScenarioInput;return p.proof})
  }
  requireProof(proof.downs>0&&proof.ups===proof.downs&&proof.cancelled===0&&proof.remaining===0&&proof.keys===0,'scenario lacks settled trusted pointer input')
  return proof
}

async function expectState(page, predicate, data, message) {
  try { await page.waitForFunction(predicate,data) } catch(error) {
    if(error.name!=='TimeoutError')throw error
    assert.fail(message)
  }
}

async function reloadBoard(page, readiness) {
  await readiness.board(page,page.url(),()=>page.reload({waitUntil:'load'}))
}

export async function preferenceScenario(page, readiness, engine='chromium') {
  const settings=page.locator('.play-settings')
  requireProof(await settings.count()===1,'settings contract unavailable')
  if(await settings.getAttribute('open')===null)await activate(page,settings.locator('summary'),true,false)
  requireProof(await page.getByRole('button',{name:'Number first',exact:true}).count()===1,'entry-order contract unavailable')
  const original=await checks(page), mode=await page.getByRole('button',{name:'Number first',exact:true}).getAttribute('aria-pressed')==='true'?'Number first':'Cell first'
  const auto=page.getByRole('checkbox',{name:'Auto-update notes',exact:true})
  requireProof(await auto.count()===1,'persisted assistance checkbox unavailable')
  const beforeBoard=await savedState(page), phases=[]
  for(const value of [false,true]){
    for(const row of original){const control=page.getByRole('checkbox',{name:row.label,exact:true});if(await control.isChecked()!==value)await tapAction(page,engine,control)}
    const expected=await checks(page)
    for(const name of ['Number first','Cell first']){
      let presentation
      const action=async()=>{
        const control=page.getByRole('button',{name,exact:true}),point=await exposeAction(page,engine,control),viewport=page.viewportSize()
        const range=await page.evaluate(()=>document.documentElement.scrollHeight-innerHeight)
        if(range>40)await panInput(page,engine,{x:4,y:4,width:viewport.width-8,height:viewport.height-8},0,Math.max(-viewport.height*.4,Math.min(viewport.height*.4,point.y-viewport.height*(value?.7:.3))))
        await exposeAction(page,engine,control)
        presentation=await page.evaluate(()=>[Math.round(scrollY),getComputedStyle(document.documentElement).fontSize])
        return tapAction(page,engine,control)
      }
      const input=value?await scaledText(page,action):await action()
      await settleViewport(page)
      assert.deepEqual(await checks(page),expected,'entry-order activation changed unrelated preferences')
      await reloadBoard(page,readiness)
      if(await settings.getAttribute('open')===null)await activate(page,settings.locator('summary'),true,false)
      assert.equal(await page.getByRole('button',{name,exact:true}).getAttribute('aria-pressed'),'true','entry order did not persist across reload')
      assert.deepEqual(await checks(page),expected,'unrelated preference changed after reload')
      phases.push({mode:name,autoNotes:value,checks:digest(expected),trusted:input.downs,presentation})
    }
  }
  // Restore only values observed in this owned browser profile, using its UI.
  for(const row of original){const control=page.getByRole('checkbox',{name:row.label,exact:true});if(await control.isChecked()!==row.checked)await activate(page,control,true,false)}
  await activate(page,page.getByRole('button',{name:mode,exact:true}),true,false)
  assertSavedState(await savedState(page),beforeBoard,'preferences changed board progress')
  return {phases:phases.length,combinations:2,reloaded:true,text:'controlled 125%',preferences:digest(phases),boardUnchanged:true}
}

export async function appearanceScenario(page, readiness, captureDark) {
  const trigger=()=>page.getByRole('button',{name:/^Appearance:/}).first()
  requireProof(await trigger().count()===1,'explicit appearance control unavailable')
  const original=await page.evaluate(()=>document.documentElement.dataset.appearance||'system'), witness=[]
  requireProof(['light','dark','system'].includes(original),'unknown original appearance')
  const choose=async value=>{
    await activate(page,trigger(),true,false)
    const option=page.getByRole('menuitemradio',{name:value[0].toUpperCase()+value.slice(1),exact:true})
    requireProof(await option.count()===1,'explicit appearance option unavailable')
    return traceInput(page,()=>activate(page,option,true,false))
  }
  try {
    for(const value of ['dark','light','system']){
      const input=await choose(value)
      await reloadBoard(page,readiness)
      const actual=await page.evaluate(()=>({choice:document.documentElement.dataset.appearance,saved:localStorage.getItem('games-site:appearance')}))
      assert.deepEqual(actual,{choice:value,saved:value},'explicit appearance did not persist across reload')
      if(value==='dark'&&captureDark)await captureDark()
      if(value==='system'){
        const palettes=[]
        for(const colorScheme of ['dark','light']){
          await page.emulateMedia({colorScheme});await settleViewport(page)
          palettes.push(await page.evaluate(()=>getComputedStyle(document.documentElement).getPropertyValue('--puzzle-page').trim()))
        }
        requireProof(palettes.every(Boolean),'system appearance palette witness unavailable')
        assert.notEqual(palettes[0],palettes[1],'System appearance did not follow the emulated color scheme')
      }
      witness.push({choice:value,trusted:input.downs,reloaded:true})
    }
  } finally {await page.emulateMedia({colorScheme:null});await choose(original)}
  return {witness}
}

async function editableCell(page) {
  for(const cell of (await cells(page).all()).slice(0,25)){
    if((await cell.textContent()).trim())continue
    await activate(page,cell,true,false)
    if(await digit(page,1).isEnabled()&&await digit(page,2).isEnabled())return cell
  }
  throw new BrowserCapabilityError('no reachable empty cell permitting both note digits')
}

function noteSet(rows,index) {
  const values=rows.filter(row=>Array.isArray(row.snapshot.notes)).map(row=>row.snapshot.notes[index])
  requireProof(values.length===1&&Array.isArray(values[0]),'saved-note witness schema unavailable')
  return [...values[0]].sort((a,b)=>a-b)
}

export async function notesScenario(page, engine, readiness) {
  const note=notesButton(page)
  requireProof(await note.count()===1,'notes control unavailable')
  if(await note.getAttribute('aria-pressed')!=='true')await activate(page,note,true,false)
  const cell=await editableCell(page), index=Number(await cell.getAttribute('data-cell'))
  await digit(page,1).scrollIntoViewIfNeeded()
  await digit(page,2).scrollIntoViewIfNeeded()
  const before=await savedState(page)
  requireProof(noteSet(before,index).length===0,'notes scenario requires a fresh empty note set')
  const view=await page.evaluate(()=>({scale:visualViewport?.scale,scroll:[scrollX,scrollY]}))
  const input=await traceInput(page,async()=>{
    if(engine==='webkit'){
      await activate(page,digit(page,1),true,false);await activate(page,digit(page,2),true,false)
      return
    }
    const a=await digit(page,1).boundingBox(),b=await digit(page,2).boundingBox()
    requireProof(a&&b,'note digits are not reachable')
    const points=[a,b].map((r,i)=>({id:i+1,x:r.x+r.width/2,y:r.y+r.height/2}))
    for(const n of [1,2])requireProof(await digit(page,n).evaluate(node=>{const r=node.getBoundingClientRect();return node.contains(document.elementFromPoint(r.x+r.width/2,r.y+r.height/2))}),'note digit is clipped or covered in the actual viewport')
    const cdp=await page.context().newCDPSession(page)
    let active=false
    try {
      await cdp.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:[points[0]]})
      active=true
      await cdp.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:points})
      await cdp.send('Input.dispatchTouchEvent',{type:'touchEnd',touchPoints:[points[1]]})
      await cdp.send('Input.dispatchTouchEvent',{type:'touchEnd',touchPoints:[]})
      active=false
    } finally {try{if(active)await cdp.send('Input.dispatchTouchEvent',{type:'touchCancel',touchPoints:[]})}finally{await cdp.detach()}}
  })
  if(engine==='chromium')requireProof(input.maxContacts===2,'trusted simultaneous contacts were not observed')
  assert.deepEqual(await page.evaluate(()=>({scale:visualViewport?.scale,scroll:[scrollX,scrollY]})),view,'notes contacts zoomed or panned the page')
  await expectState(page,({index})=>[...document.querySelectorAll('[data-cell]')].some(n=>Number(n.dataset.cell)===index&&/notes\s+1,\s*2/.test(n.getAttribute('aria-label')||'')),{index},'both intended notes are not visible')
  const edited=await savedState(page)
  assert.deepEqual(noteSet(edited,index),[1,2],'overlap lost or duplicated a saved note')
  const visible=await renderedState(page)
  await activate(page,page.getByRole('button',{name:'Undo',exact:true}),true,false)
  const undone=noteSet(await savedState(page),index)
  assert.ok(undone.length===1&&[1,2].includes(undone[0]),'Undo did not remove exactly one settled overlapping note')
  await activate(page,page.getByRole('button',{name:'Redo',exact:true}),true,false)
  assertSavedState(await savedState(page),edited,'Redo did not restore both notes')
  await reloadBoard(page,readiness)
  assertSavedState(await savedState(page),edited,'notes did not survive reload')
  assertRenderedState(await renderedState(page),visible,'visible notes did not survive reload')
  const sequences=[]
  if(await notesButton(page).getAttribute('aria-pressed')!=='true')await tapAction(page,engine,notesButton(page))
  for(const n of [1,2,1,2]){await tapAction(page,engine,cell);await tapAction(page,engine,digit(page,n));sequences.push(noteSet(await savedState(page),index))}
  assert.deepEqual(sequences,[[2],[],[1],[1,2]],'repeated single-finger note add/remove did not apply each intent once')
  const gestures=engine==='chromium'?await digitGestureScenario(page,engine):'unsupported touch hold/drag/cancel'
  return {status:engine==='chromium'?'passed':'partial',contacts:input.maxContacts,notes:[1,2],undo:undone,observed:digest([edited,sequences]),gestures,reloaded:true}
}

export async function shikakuScenario(page, readiness) {
  const board=page.locator('.shikaku-board'), data=await board.locator('[data-cell]').allTextContents(), n=Math.sqrt(data.length)
  requireProof(Number.isInteger(n)&&n<=10,'fresh small Shikaku geometry unavailable')
  const clues=data.map(t=>/^\s*\d+\s*$/.test(t)?Number(t):0), singleton=clues.indexOf(1)
  const rectangles=[]
  for(let a=0;a<data.length;a++)for(let b=a;b<data.length;b++){
    if(a%n>b%n)continue
    const indexes=[]
    for(let r=Math.floor(a/n);r<=Math.floor(b/n);r++)for(let c=a%n;c<=b%n;c++)indexes.push(r*n+c)
    const numbered=indexes.filter(i=>clues[i]>0)
    if(indexes.length>1&&numbered.length===1&&clues[numbered[0]]===indexes.length)rectangles.push({a,b,indexes,clue:numbered[0]})
  }
  const rectangle=rectangles.find(r=>rectangles.some(other=>other.clue===r.clue&&other.a!==r.a))
  requireProof(singleton>=0&&rectangle,'catalogue sample lacks singleton/rectangle witnesses')
  const replacement=rectangles.find(r=>r.clue===rectangle.clue&&r.a!==rectangle.a)
  const painted=()=>board.locator('[data-cell]').evaluateAll(nodes=>nodes.filter(n=>n.style.getPropertyValue('--rectangle-fill').trim()).map(n=>Number(n.dataset.cell)))
  const baseline=await savedState(page)
  requireProof((await painted()).length===0,'Shikaku requires a fresh board')
  const witnesses=[]
  for(const {a,b,indexes} of [{a:singleton,b:singleton,indexes:[singleton]},rectangle]){
    const input=await traceInput(page,async()=>{
      await activate(page,board.locator(`[data-cell="${a}"]`),true,false)
      await activate(page,board.locator(`[data-cell="${b}"]`),true,false)
      await activate(page,page.getByRole('button',{name:'Add rectangle',exact:true}),true,false)
    })
    await settleViewport(page)
    assert.deepEqual(await painted(),indexes,'fresh Add did not commit exactly the selected rectangle')
    const saved=await savedState(page)
    await activate(page,page.getByRole('button',{name:'Undo',exact:true}),true,false)
    assert.deepEqual(await painted(),[],'rectangle Undo left painted cells')
    await activate(page,page.getByRole('button',{name:'Redo',exact:true}),true,false)
    assertSavedState(await savedState(page),saved,'rectangle Redo changed saved state')
    await reloadBoard(page,readiness)
    assert.deepEqual(await painted(),indexes,'rectangle reload lost visible coverage')
    assertSavedState(await savedState(page),saved,'rectangle reload lost saved coverage')
    const anchor=board.locator(`[data-cell="${a}"]`)
    await activate(page,anchor,true,false)
    const revise=page.getByRole('button',{name:'Revise selected',exact:true}),remove=page.getByRole('button',{name:'Remove selected',exact:true})
    requireProof(await revise.count()===1&&await remove.count()===1,'Shikaku revise/remove contract unavailable')
    await activate(page,revise,true,false)
    await activate(page,page.getByRole('button',{name:'Cancel preview',exact:true}),true,false)
    assertSavedState(await savedState(page),saved,'canceling a revision changed committed coverage')
    if(indexes.length>1){
      await activate(page,anchor,true,false);await activate(page,revise,true,false)
      for(const i of [replacement.a,replacement.b])await activate(page,board.locator(`[data-cell="${i}"]`),true,false)
      await activate(page,page.getByRole('button',{name:'Replace rectangle',exact:true}),true,false)
      assert.deepEqual(await painted(),replacement.indexes,'revision did not replace exactly the intended rectangle')
      assert.notEqual(digest(await savedState(page)),digest(saved),'revision did not change the committed rectangle')
      await activate(page,page.getByRole('button',{name:'Undo',exact:true}),true,false)
      assertSavedState(await savedState(page),saved,'revision Undo did not restore the original rectangle')
    }
    await activate(page,anchor,true,false);await activate(page,remove,true,false)
    assert.deepEqual(await painted(),[],'Remove selected did not remove the intended rectangle')
    await activate(page,page.getByRole('button',{name:'Undo',exact:true}),true,false)
    assertSavedState(await savedState(page),saved,'Undo did not restore the removed rectangle')
    witnesses.push({cells:indexes.length,saved:digest(saved),input:{touches:input.downs,contacts:input.maxContacts}})
    if(indexes.length===1){
      for(const i of [rectangle.a,rectangle.b])await activate(page,board.locator(`[data-cell="${i}"]`),true,false)
      await activate(page,page.getByRole('button',{name:'Add rectangle',exact:true}),true,false)
      assert.deepEqual((await painted()).sort((a,b)=>a-b),[singleton,...rectangle.indexes].sort((a,b)=>a-b),'after-edit rectangle commit replaced unrelated singleton coverage')
      const combined=await savedState(page)
      await activate(page,page.getByRole('button',{name:'Undo',exact:true}),true,false)
      assertSavedState(await savedState(page),saved,'after-edit Undo changed previous coverage')
      await activate(page,page.getByRole('button',{name:'Redo',exact:true}),true,false)
      assertSavedState(await savedState(page),combined,'after-edit Redo lost combined coverage')
      // Reload intentionally loses in-tab history. Restart through the actual UI.
      await activate(page,page.getByRole('button',{name:'Restart',exact:true}),true,false)
      const confirm=page.getByRole('dialog').getByRole('button',{name:'Restart',exact:true})
      await activate(page,confirm,true,false)
      await reloadBoard(page,readiness)
      assert.deepEqual(await painted(),[],'restart did not produce a fresh rectangle board')
      assertSavedState(await savedState(page),baseline,'restart changed the fresh snapshot')
    }
  }
  return {areas:witnesses.map(w=>w.cells),states:digest(witnesses),history:true,afterEdits:true,reviseReplaceUndo:true,removeCancel:true}
}

export async function privacyScenario(page) {
  const link=page.locator('.privacy-choices a[href="/privacy"]').first()
  requireProof(await link.count()===1,'first-party privacy link unavailable')
  await activate(page,link,true,false)
  await page.waitForURL(url=>url.pathname==='/privacy')
  requireProof(await page.locator('.privacy-choices').count()===1,'first-party privacy choices unavailable')
  const button=page.getByRole('button',{name:'Privacy choices',exact:true})
  requireProof(await button.count()===1,'first-party privacy control unavailable')
  const input=await traceInput(page,()=>activate(page,button,true,false))
  assert.equal(await page.locator('script[src*="googletagmanager.com"],script[src*="googlesyndication.com"]').count(),0,'optional tags were inserted without an eligible consent context')
  return {status:'partial',firstParty:'privacy page/control reached',input:{touches:input.downs},vendor:'unverified: canonical origin, enabled meta and scoped vendor egress required'}
}

export async function reachablePrivacyControls(page) {
  for(const name of ['Undo','Redo','Notes']){
    const control=page.getByRole('button',{name:new RegExp('^'+name+'(?:\\s|$)')}).first(), r=await control.boundingBox()
    requireProof(r,'privacy fixture control unavailable')
    assert.equal(await control.evaluate(node=>{const r=node.getBoundingClientRect();return node.contains(document.elementFromPoint(r.x+r.width/2,r.y+r.height/2))}),true,'privacy surface covers '+name)
  }
}

async function storageObservation(page, action, key) {
  await page.evaluate(()=>{
    const keys=[]
    const observe=e=>{if(e.isTrusted&&e.key?.startsWith('games-site:')&&keys.length<16)keys.push(e.key)}
    window.runnerScenarioStorage={keys,stop(){window.removeEventListener('storage',observe)}}
    window.addEventListener('storage',observe)
  })
  try {
    await action()
    await expectState(page,key=>window.runnerScenarioStorage.keys.includes(key),key,'native cross-tab storage event was not observed')
    await settleViewport(page)
  } finally {await page.evaluate(()=>{window.runnerScenarioStorage.stop();delete window.runnerScenarioStorage})}
}

async function markNonogram(page, index, shade) {
  await activate(page,page.locator(`.nonogram-board [data-cell="${index}"]`),true,false)
  return traceInput(page,()=>activate(page,page.getByRole('button',{name:shade?/Shade/:/Empty/}).first(),true,false))
}

export async function hintContinuityScenario(page, readiness, unrelatedPath, capture, engine='chromium') {
  requireProof(/^\/binary\/[A-Za-z0-9_-]+$/.test(unrelatedPath),'admitted unrelated board path unavailable')
  const panel=page.locator('.checked-hint'), reveal=page.getByRole('button',{name:'Show the next move',exact:true})
  if(await reveal.count()===0)await requestCheckedHint(page,'nonogram',engine)
  requireProof(await reveal.count()===1,'revealed Nonogram hint contract unavailable')
  await activate(page,reveal,true,false)
  const apply=()=>panel.getByRole('button',{name:/^Apply \d+ changes?$/})
  requireProof(await apply().count()===1,'actionable revealed hint unavailable')
  const before=(await savedState(page))
  const url=page.url(),matching=before.filter(r=>r.key.includes(new URL(url).pathname.split('/').at(-1))),current=matching.length===1?matching[0]:before.length===1?before[0]:null
  requireProof(current,'one saved board bound to the revealed hint is required')
  const key=current.key, hint=digest(await panel.innerText())
  const other=await page.context().newPage()
  other.setDefaultTimeout(page.getDefaultTimeout?.()||20000)
  let denied=false
  try {
    readiness.watch(other,new URL(url).origin)
    await readiness.board(other,new URL(unrelatedPath,url).href)
    const unrelatedBefore=await savedState(other)
    const otherKey=unrelatedBefore.find(row=>!before.some(old=>old.key===row.key))?.key
    requireProof(otherKey,'unrelated board storage witness unavailable')
    await storageObservation(page,async()=>{
      let cell
      for(const candidate of await cells(other).all())if(!(await candidate.textContent()).trim()){cell=candidate;break}
      requireProof(cell,'unrelated editable cell unavailable')
      await activate(other,cell,true,false)
      await traceInput(other,()=>activate(other,digit(other,1),true,false))
      const after=await savedState(other)
      assert.notEqual(digest(after.find(r=>r.key===otherKey)?.snapshot),digest(unrelatedBefore.find(r=>r.key===otherKey)?.snapshot),'unrelated tab did not save a real edit')
    },otherKey)
    assert.equal(await apply().isVisible(),true,'unrelated tab save cancelled the revealed hint')
    assert.equal(digest(await panel.innerText()),hint,'unrelated tab save changed revealed hint contents')
    assert.deepEqual((await savedState(page)).find(r=>r.key===key),current,'unrelated tab changed current board storage')
    const previousPath=new URL(other.url()).pathname,newBoard=other.getByRole('button',{name:'New puzzle',exact:true})
    requireProof(await newBoard.count()===1,'unrelated New puzzle control unavailable')
    await readiness.newBoard(other,'binary',async()=>{
      const response=other.waitForResponse(r=>new URL(r.url()).pathname==='/api/games/next'&&r.request().method()==='POST')
      await tapAction(other,engine,newBoard);assert.equal((await response).status(),200,'unrelated New failed')
      await other.waitForURL(url=>url.pathname.startsWith('/binary/')&&url.pathname!==previousPath)
    })
    assert.equal(await apply().isVisible(),true,'unrelated New cancelled the revealed hint')
    assert.equal(digest(await panel.innerText()),hint,'unrelated New changed the revealed hint')
    assert.deepEqual((await savedState(page)).find(r=>r.key===key),current,'unrelated New changed current board progress')
    await capture('hint')
    await readiness.board(other,url)
    // Controlled, page-local write denial creates a genuine dirty local edit.
    // The second tab still uses the browser's native Storage implementation.
    await page.evaluate(key=>{
      const original=Storage.prototype.setItem
      window.runnerScenarioDenied={restore(){Storage.prototype.setItem=original;delete window.runnerScenarioDenied}}
      Storage.prototype.setItem=function(k,v){if(k===key)throw new DOMException('Owned scenario write denial','QuotaExceededError');return original.call(this,k,v)}
    },key)
    denied=true
    await markNonogram(page,0,true)
    requireProof(await page.getByRole('button',{name:'Retry save',exact:true}).isVisible(),'controlled page-local write denial did not produce a dirty recovery witness')
    const local=await renderedState(page)
    await storageObservation(page,async()=>{await markNonogram(other,1,false)},key)
    const replacement=page.getByRole('button',{name:'Replace saved copy…',exact:true})
    assert.equal(await replacement.isVisible(),true,'same-board competing save did not expose conflict recovery')
    assertRenderedState(await renderedState(page),local,'same-board competing save silently replaced dirty local entries')
    assert.equal(await apply().count(),0,'stale hint stayed applicable during same-board conflict')
    const remote=await savedState(other)
    assert.deepEqual(await savedState(page),remote,'conflict overwrote the remote saved copy')
    await capture('hint-conflict')
    return {unrelatedSaveNew:true,events:'trusted storage',conflict:{setup:'page-local write denial',local:digest(local),remote:digest(remote),staleApplyAbsent:true}}
  } finally {
    if(denied&&!page.isClosed())await page.evaluate(()=>window.runnerScenarioDenied?.restore())
    await other.close()
  }
}

// Observe the admission response already consumed by the candidate UI. The
// native-image gate owns answer validity; this lane owns actual UI completion.
// No direct API call, solver, repository script or state injection is used.
export async function admittedRecipe(page, game, readiness) {
  const route=new URL(page.url()), code=route.pathname.split('/').at(-1), rows=[]
  const observe=response=>{
    const request=response.request(), url=new URL(response.url())
    if(url.origin!==route.origin||url.pathname!=='/api/games/'+(['sudoku','suguru'].includes(game)?'admit':game)||request.method()!=='POST'||response.status()!==200)return
    let payload
    try {payload=request.postDataJSON()}catch{return}
    if(payload?.kind!=='reopen'||payload.identity?.game!==game||typeof payload.identity.id!=='string'||payload.identity.id.split(':').at(-1)!==code)return
    const pending=(async()=>{
      const body=await response.body()
      requireProof(body.length<=1024*1024,'completion admission envelope exceeds its existing UI bound')
      const envelope=JSON.parse(body)
      requireProof(envelope.status==='success'&&digest(envelope.request)===digest(payload),'completion admission request binding unavailable')
      const transfer=envelope.publication||envelope.result?.publication, identity=transfer?.puzzle||transfer?.identity
      requireProof(identity&&['game','id','contentRevision'].every(key=>identity[key]===payload.identity[key]),'completion publication differs from the requested full identity')
      return transfer
    })()
    pending.catch(()=>{}) // joined below, including failed navigation
    rows.push(pending)
  }
  page.on('response',observe)
  let navigationError
  try {await reloadBoard(page,readiness)} catch(error){navigationError=error} finally {page.off('response',observe)}
  const results=await Promise.allSettled(rows)
  if(navigationError)throw navigationError
  requireProof(results.length===1,'one normal candidate-bound completion publication is required')
  if(results[0].status==='rejected')throw results[0].reason
  const transfer=results[0].value, definition=transfer?.puzzle||transfer?.definition, identity=transfer?.puzzle||transfer?.identity
  requireProof(identity?.game===game&&typeof identity.id==='string'&&identity.id.split(':').at(-1)===code&&identity.contentRevision==='1'&&Array.isArray(transfer.answer),'completion publication identity/answer unavailable')
  requireProof(definition&&typeof definition==='object'&&!Array.isArray(definition)&&(game!=='kakuro'||Array.isArray(definition.board?.cells)),'completion definition schema unavailable')
  const n=typeof definition.rows==='number'?definition.rows:definition.size||definition.board?.size
  const height=typeof definition.rows==='number'?definition.rows:n, columns=typeof definition.columns==='number'?definition.columns:n
  requireProof([height,columns].every(v=>Number.isInteger(v)&&v>=4&&v<=30),'bounded admitted board geometry unavailable')
  const answer=transfer.answer
  if(game==='shikaku')requireProof(answer.length>0&&answer.length<=n*n&&answer.every(r=>r&&[r.top,r.left,r.bottom,r.right].every(Number.isInteger)&&r.top>=0&&r.left>=0&&r.bottom>=r.top&&r.right>=r.left&&r.bottom<n&&r.right<n),'completion rectangle recipe unavailable')
  else if(game==='hashi')requireProof(Array.isArray(definition.islands)&&definition.islands.length<=169&&answer.length<=338&&answer.every(b=>b&&[b.a,b.b,b.count].every(Number.isInteger)&&b.a>=0&&b.a<b.b&&b.b<definition.islands.length&&b.count>=0&&b.count<=2),'completion bridge recipe unavailable')
  else requireProof(answer.length===height*columns&&answer.every(v=>game==='nonogram'?['filled','empty'].includes(v):game==='binary'?['zero','one'].includes(v):Number.isInteger(v)&&v>=(game==='kakuro'?0:1)&&v<=30),'completion cell recipe unavailable')
  return {game,n,rows:height,columns,identity:{game,id:identity.id,contentRevision:identity.contentRevision},definition,answer,publication:digest(transfer)}
}

export async function completionRecipe(page, game, readiness) {
  const recipe=await admittedRecipe(page,game,readiness)
  requireProof(recipe.rows===recipe.columns&&recipe.n<=9,'representative completion requires a bounded small square board')
  return recipe
}

export async function completeRepresentative(page, recipe, readiness, capture) {
  const {game,n,definition,answer}=recipe, frame=page.locator('.player-board-frame:visible').first()
  const nodes=await frame.locator(game==='hashi'?'[data-island]':'button[data-cell]').evaluateAll(nodes=>nodes.map(node=>({id:Number(node.dataset.cell??node.dataset.island),label:node.getAttribute('aria-label')||'',given:node.classList.contains('given')||node.dataset.given==='true',text:node.textContent.trim()})))
  requireProof(nodes.length===(game==='hashi'?definition.islands.length:game==='kakuro'?definition.board.cells.filter(c=>c.white).length:n*n)&&new Set(nodes.map(row=>row.id)).size===nodes.length&&nodes.every(row=>Number.isInteger(row.id)&&row.id>=0&&row.id<(game==='hashi'?nodes.length:n*n)&&(game!=='kakuro'||definition.board.cells[row.id]?.white)),'visible completion board does not match admission geometry')
  requireProof(await page.locator('.completion:visible,.solved-status:visible').count()===0,'representative completion requires an unsolved fresh board')
  if(game==='hashi')for(const row of nodes){const island=definition.islands[row.id];requireProof(row.label.toLowerCase().includes(`row ${island.y+1}, column ${island.x+1}, needs ${island.degree}`),'visible island differs from admitted recipe')}
  const input=await traceInput(page,async()=>{
    if(game==='shikaku'){
      for(const r of answer){
        for(const i of [r.top*n+r.left,r.bottom*n+r.right])await activate(page,frame.locator(`[data-cell="${i}"]`),false,false)
        await activate(page,page.getByRole('button',{name:'Add rectangle',exact:true}),false,false)
      }
    }else if(game==='hashi'){
      for(const b of answer.filter(b=>b.count)){
        const cancel=page.getByRole('button',{name:'Choose another neighbor',exact:true})
        if(await cancel.count())await activate(page,cancel,false,false)
        for(const i of [b.a,b.b])await activate(page,frame.locator(`[data-island="${i}"]`),false,false)
        await activate(page,page.getByRole('button',{name:b.count===1?'One bridge':'Two bridges',exact:true}),false,false)
      }
    }else{
      for(const row of nodes){
        if(row.given||/starting clue|given, fixed/i.test(row.label))continue
        const value=answer[row.id]
        if(game==='nonogram'&&value==='empty')continue // crosses are optional
        requireProof(value!==undefined&&(game!=='kakuro'||definition.board.cells[row.id]?.white),'completion cell identity differs from admitted recipe')
        await activate(page,frame.locator(`[data-cell="${row.id}"]`),false,false)
        const control=game==='nonogram'?page.getByRole('button',{name:/Shade/}):digit(page,game==='binary'?value==='one'?1:0:value)
        requireProof(await control.count()===1,'completion entry control unavailable')
        await activate(page,control,false,false)
      }
    }
  },false)
  await expectState(page,()=>!!document.querySelector('.completion-dialog[open]')&&[...document.querySelectorAll('.completion h2,.solved-status')].some(n=>/^(?:Puzzle solved!|✓?\s*Solved)$/.test(n.textContent.trim())),undefined,'normal UI entries did not produce actual solved state and celebration')
  await activate(page,page.locator('.completion-dialog[open]').getByRole('button',{name:'View board',exact:true}),false,false)
  const saved=await savedState(page), visible=await renderedState(page)
  await reloadBoard(page,readiness)
  assertSavedState(await savedState(page),saved,'solved progress changed after reload')
  assertRenderedState(await renderedState(page),visible,'solved board changed after reload')
  assert.equal(await page.locator('.completion:visible,.solved-status:visible').count(),1,'actual solved result did not survive reload')
  await capture()
  return {result:digest([recipe.identity,recipe.publication,saved]),clicks:input.downs,solved:true,reloaded:true}
}

// Read-only hit/clip measurements accompany real browser input. A locator being
// visible is insufficient: the intended point must be on screen and uncovered.
export async function actionPoint(page, locator) {
  requireProof(await locator.count()===1,'one fixed action/target is required')
  return locator.evaluate(node=>{
    const r=node.getBoundingClientRect(), x=r.x+r.width/2,y=r.y+r.height/2
    let left=0,top=0,right=innerWidth,bottom=innerHeight
    for(let p=node.parentElement;p;p=p.parentElement){const s=getComputedStyle(p),b=p.getBoundingClientRect();if(/auto|scroll|hidden|clip/.test(s.overflowX)){left=Math.max(left,b.left+p.clientLeft);right=Math.min(right,b.left+p.clientLeft+p.clientWidth)}if(/auto|scroll|hidden|clip/.test(s.overflowY)){top=Math.max(top,b.top+p.clientTop);bottom=Math.min(bottom,b.top+p.clientTop+p.clientHeight)}}
    const style=getComputedStyle(node),hit=r.width>0&&r.height>0&&style.visibility==='visible'&&Number(style.opacity)>0&&x>=left&&x<right&&y>=top&&y<bottom&&node.contains(document.elementFromPoint(x,y))
    return {x,y,width:r.width,height:r.height,font:parseFloat(style.fontSize),hit}
  })
}

async function panInput(page,engine,region,dx,dy) {
  const x=Math.round(region.x+region.width*(dx>0?.8:dx<0?.2:.5)),y=Math.round(region.y+region.height*(dy>0?.8:dy<0?.2:.5))
  await page.evaluate(()=>{
    const p={touch:0,wheel:0},touch=e=>{if(e.isTrusted)p.touch++},wheel=e=>{if(e.isTrusted)p.wheel++}
    document.addEventListener('touchstart',touch,{capture:true,passive:true});document.addEventListener('wheel',wheel,{capture:true,passive:true})
    window.runnerPanWitness={p,stop(){document.removeEventListener('touchstart',touch,true);document.removeEventListener('wheel',wheel,true)}}
  })
  let input
  try {
    if(engine==='chromium'){
      const cdp=await page.context().newCDPSession(page)
      let active=false
      try {
        await cdp.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:[{id:1,x,y}]});active=true
        for(let step=1;step<=4;step++){
          await cdp.send('Input.dispatchTouchEvent',{type:'touchMove',touchPoints:[{id:1,x:x-dx*step/4,y:y-dy*step/4}]})
          await settleViewport(page)
        }
        await cdp.send('Input.dispatchTouchEvent',{type:'touchEnd',touchPoints:[]});active=false
      }finally{try{if(active)await cdp.send('Input.dispatchTouchEvent',{type:'touchCancel',touchPoints:[]})}finally{await cdp.detach()}}
    }else{
      await page.mouse.move(x,y)
      try{await page.mouse.wheel(dx,dy)}catch(error){if(engine==='webkit'&&error.message.includes('Mouse wheel is not supported in mobile WebKit'))throw new BrowserCapabilityError('mobile WebKit exposes neither trusted touch drag nor wheel positioning');throw error}
    }
    await settleViewport(page)
  } finally {input=await page.evaluate(()=>{const w=window.runnerPanWitness;w.stop();delete window.runnerPanWitness;return w.p})}
  requireProof(engine==='chromium'?input.touch>0:input.wheel>0,'trusted pan/positioning provenance unavailable')
  return input
}

// Bounded native scrolling to a fixed UI target. WebKit positioning is explicitly
// a mouse wheel, followed by trusted taps, never described as finger panning.
export async function exposeAction(page,engine,locator) {
  const trace=[]
  for(let attempt=0;attempt<16;attempt++){
    const point=await actionPoint(page,locator)
    if(point.hit)return point
    const step=await locator.evaluate(node=>{
      const target=node.getBoundingClientRect(),viewport={x:4,y:4,width:innerWidth-8,height:innerHeight-8}
      for(let p=node.parentElement;p&&p!==document.body&&p!==document.documentElement;p=p.parentElement){
        const s=getComputedStyle(p),r=p.getBoundingClientRect(),scrollX=/auto|scroll/.test(s.overflowX)&&p.scrollWidth>p.clientWidth+1,scrollY=/auto|scroll/.test(s.overflowY)&&p.scrollHeight>p.clientHeight+1
        if(!scrollX&&!scrollY)continue
        const left=Math.max(4,r.left+p.clientLeft),top=Math.max(4,r.top+p.clientTop),right=Math.min(innerWidth-4,r.left+p.clientLeft+p.clientWidth),bottom=Math.min(innerHeight-4,r.top+p.clientTop+p.clientHeight)
        if(right-left<40||bottom-top<40)break
        const dx=scrollX?target.x+target.width/2-(left+right)/2:0,dy=scrollY?target.y+target.height/2-(top+bottom)/2:0
        const canX=dx<0?p.scrollLeft>1:p.scrollLeft+p.clientWidth<p.scrollWidth-1,canY=dy<0?p.scrollTop>1:p.scrollTop+p.clientHeight<p.scrollHeight-1
        if((Math.abs(dx)>8&&canX)||(Math.abs(dy)>8&&canY))return {region:{x:left,y:top,width:right-left,height:bottom-top},dx:Math.max(-(right-left)*.6,Math.min((right-left)*.6,dx)),dy:Math.max(-(bottom-top)*.6,Math.min((bottom-top)*.6,dy))}
      }
      const dx=target.x<0||target.right>innerWidth?target.x+target.width/2-innerWidth/2:0,dy=target.y+target.height/2-innerHeight/2
      return {region:viewport,dx:Math.max(-innerWidth*.6,Math.min(innerWidth*.6,dx)),dy:Math.max(-innerHeight*.6,Math.min(innerHeight*.6,dy))}
    })
    requireProof(Math.abs(step.dx)>8||Math.abs(step.dy)>8,'target is covered or unreachable by supported scrolling')
    const before=await savedState(page)
    await panInput(page,engine,step.region,step.dx,step.dy)
    trace.push({point:[Math.round(point.x),Math.round(point.y)],step:[Math.round(step.dx),Math.round(step.dy)],scroll:await page.evaluate(()=>[scrollX,scrollY,...[...document.querySelectorAll('.player-board-frame')].flatMap(n=>[n.scrollLeft,n.scrollTop])])})
    assertSavedState(await savedState(page),before,'navigation pan unexpectedly edited progress')
  }
  assert.fail('fixed target remained unreachable after bounded native scrolling: '+JSON.stringify(trace.slice(-4)))
}

async function tapAction(page,engine,locator) {
  await exposeAction(page,engine,locator)
  return traceInput(page,()=>activate(page,locator,true))
}

async function scaledText(page,action) {
  // Explicit controlled browser presentation, not OS/browser text preference proof.
  const style=await page.addStyleTag({content:'html{font-size:125% !important}'})
  try {await settleViewport(page);return await action()}finally{await style.evaluate(node=>node.remove());await settleViewport(page)}
}

async function intentionalPinch(page) {
  const before=await savedState(page),scale=await page.evaluate(()=>visualViewport.scale),cdp=await page.context().newCDPSession(page)
  await page.evaluate(()=>{
    const observe=e=>{if(e.isTrusted)window.runnerPinch.contacts=Math.max(window.runnerPinch.contacts,e.touches.length)}
    window.runnerPinch={contacts:0,stop(){document.removeEventListener('touchstart',observe,true)}}
    document.addEventListener('touchstart',observe,{capture:true,passive:true})
  })
  let enlarged
  try {
    const {width,height}=page.viewportSize()
    await cdp.send('Input.synthesizePinchGesture',{x:width/2,y:height/2,scaleFactor:1.3,gestureSourceType:'touch'})
    await settleViewport(page)
    requireProof(await page.evaluate(()=>window.runnerPinch.contacts>=2),'trusted browser pinch contacts unavailable')
    enlarged=await page.evaluate(()=>visualViewport.scale)
    requireProof(enlarged>scale+.1,'trusted pinch contacts were delivered but this headless runtime did not enlarge the visual viewport')
    assertSavedState(await savedState(page),before,'intentional pinch edited puzzle progress')
  }finally{
    try {
      const current=await page.evaluate(()=>visualViewport.scale)
      if(current>scale+.01)await cdp.send('Input.synthesizePinchGesture',{x:page.viewportSize().width/2,y:page.viewportSize().height/2,scaleFactor:scale/current,gestureSourceType:'touch'})
    }finally{await cdp.detach();await page.evaluate(()=>{window.runnerPinch.stop();delete window.runnerPinch})}
  }
  await settleViewport(page)
  assert.ok(Math.abs((await page.evaluate(()=>visualViewport.scale))-scale)<.02,'intentional pinch did not restore the owned viewport')
  return [scale,Math.round(enlarged*100)/100]
}

function activeProgress(rows,pagePath) {
  const code=pagePath.split('/').at(-1),matches=rows.filter(r=>r.key.includes(code))
  requireProof(matches.length===1,'one saved snapshot bound to the current canonical code is required')
  return matches[0]
}

async function currentSelected(locator) {
  return locator.evaluate(n=>n.getAttribute('aria-pressed')==='true'||n.getAttribute('aria-selected')==='true'||n.classList.contains('selected'))
}

async function enterCell(page,engine,recipe,index) {
  const target=page.locator(`.player-board-frame button[data-cell="${index}"]`),before=await savedState(page)
  await tapAction(page,engine,target)
  assert.equal(await currentSelected(target),true,'trusted selection chose a different cell')
  const answer=recipe.answer[index],control=recipe.game==='nonogram'?page.getByRole('button',{name:answer==='filled'?/Shade/:/Empty/}).first():digit(page,recipe.game==='binary'?answer==='one'?1:0:answer)
  await tapAction(page,engine,control)
  const expected=structuredClone(before),row=activeProgress(expected,new URL(page.url()).pathname),field=Array.isArray(row.snapshot.entries)?'entries':Array.isArray(row.snapshot.values)?'values':null
  requireProof(field,'saved cell-entry schema unavailable')
  row.snapshot[field][index]=answer
  if(row.snapshot.notes)row.snapshot.notes[index]=[]
  assertSavedState(await savedState(page),expected,'input did not change exactly the intended distant cell')
  await tapAction(page,engine,page.getByRole('button',{name:'Undo',exact:true}))
  assertSavedState(await savedState(page),before,'distant/landscape correction did not restore the exact board')
  return digest(expected)
}

async function mappedClues(page,recipe,index) {
  const row=Math.floor(index/recipe.columns)+1,column=index%recipe.columns+1,proof=[]
  requireProof(Array.isArray(recipe.definition.rows)&&Array.isArray(recipe.definition.columns),'admitted Nonogram clue mapping unavailable')
  for(const [axis,number,expected] of [['row',row,recipe.definition.rows[row-1]],['column',column,recipe.definition.columns[column-1]]]){
    const candidates=page.locator('[aria-label]').filter({hasNot:page.locator('button[data-cell]')})
    const indexes=await candidates.evaluateAll((nodes,{axis,number})=>nodes.flatMap((node,i)=>{
      const name=node.getAttribute('aria-label').toLowerCase()
      return new RegExp('(?:^|selected )'+axis+' '+number+'(?: clues|:)').test(name)?[i]:[]
    }),{axis,number})
    requireProof(indexes.length>0,'semantic selected '+axis+' clue/readout mapping unavailable')
    let matched=false
    for(const i of indexes){
      const node=candidates.nth(i),point=await actionPoint(page,node)
      if(!point.hit)continue
      const text=await node.innerText(),values=(text.match(/\d+/g)||[]).map(Number),wanted=expected.length?expected:[0]
      const visible=JSON.stringify(values)===JSON.stringify(wanted)||JSON.stringify(values)===JSON.stringify([number,...wanted])
      assert.equal(visible,true,'visible '+axis+' clues do not match the current admitted cell')
      assert.ok(point.font>=12,'panned reference clues/readout are too small to read')
      proof.push([axis,number,digest(wanted)]);matched=true;break
    }
    assert.equal(matched,true,'mapped '+axis+' clues/readout are clipped or covered in the panned viewport')
  }
  return digest(proof)
}

export async function largeBoardScenario(page,game,engine,readiness,capture) {
  const recipe=await admittedRecipe(page,game,readiness),before=await savedState(page),frame=page.locator('.player-board-frame:visible').first()
  const header=await headerWitness(page)
  const zoom=page.getByRole('button',{name:'Zoom in',exact:true}),fit=page.getByRole('button',{name:'Fit board',exact:true})
  requireProof(await zoom.count()===1&&await fit.count()===1,'discoverable board zoom/fit controls unavailable')
  const board=frame.locator('.puzzle-board,.nonogram-board,.binary-board,.futoshiki-board,.shikaku-board,.hashi-board,.calcudoku-board,.kakuro-board').first()
  await tapAction(page,engine,fit)
  const overview=await board.boundingBox()
  await tapAction(page,engine,zoom)
  const zoomed=await board.boundingBox()
  requireProof(zoomed&&zoomed.width>0,'zoomed board unavailable')
  assert.ok(zoomed.width>overview.width+1,'discoverable Zoom did not enlarge the explicit overview')
  assertSavedState(await savedState(page),before,'Zoom unexpectedly changed entries')
  const move=page.getByRole('button',{name:'Move board',exact:true}),wasMoving=await move.count()?await move.getAttribute('aria-pressed')==='true':false
  if(game==='shikaku'&&!wasMoving)await tapAction(page,engine,move)
  const positions=[],startScroll=await frame.evaluate(n=>[n.scrollLeft,n.scrollTop])
  let failed=false
  try {
    const selector=game==='hashi'?'button[data-island]':'button[data-cell]'
    const targets=await board.locator(selector).evaluateAll(nodes=>nodes.map(n=>({id:Number(n.dataset.cell??n.dataset.island),given:n.classList.contains('given')||n.dataset.given==='true'||/starting clue|given, fixed/.test(n.getAttribute('aria-label')||'')})).filter(n=>!n.given))
    requireProof(targets.length>=2,'two adjacent editable large-board targets unavailable')
    const ids=new Set(targets.map(t=>t.id))
    let far,adjacent
    if(game==='hashi'){
      far=targets.at(-1).id
      const origin=recipe.definition.islands[far]
      adjacent=targets.filter(t=>t.id!==far).sort((a,b)=>{
        const distance=i=>Math.abs(recipe.definition.islands[i].x-origin.x)+Math.abs(recipe.definition.islands[i].y-origin.y)
        return distance(a.id)-distance(b.id)
      })[0]?.id
    }else{
      for(const target of [...targets].reverse()){
        const neighbours=[target.id-1,target.id+1,target.id-recipe.columns,target.id+recipe.columns].filter(i=>ids.has(i)&&Math.abs(Math.floor(i/recipe.columns)-Math.floor(target.id/recipe.columns))+Math.abs(i%recipe.columns-target.id%recipe.columns)===1)
        if(neighbours.length){far=target.id;adjacent=neighbours[0];break}
      }
      requireProof(far!==undefined&&(Math.floor(far/recipe.columns)>=recipe.rows-2||far%recipe.columns>=recipe.columns-2),'far-edge adjacent editable targets unavailable')
    }
    requireProof(adjacent!==undefined,'adjacent large-board target unavailable')
    const middle=targets.reduce((best,t)=>Math.abs(t.id-(Math.floor(recipe.rows/2)*recipe.columns+Math.floor(recipe.columns/2)))<Math.abs(best.id-(Math.floor(recipe.rows/2)*recipe.columns+Math.floor(recipe.columns/2)))?t:best).id
    for(const index of [...new Set(game==='nonogram'?[middle,far,adjacent]:[far,adjacent])]){
      const target=board.locator(`[${game==='hashi'?'data-island':'data-cell'}="${index}"]`)
      const panBefore=await savedState(page),point=await exposeAction(page,engine,target)
      assertSavedState(await savedState(page),panBefore,'large-board pan made an unintended entry')
      assert.ok(point.width>=24&&point.height>=24,'Zoom did not provide a representative operable target size')
      await tapAction(page,engine,target)
      assert.equal(await currentSelected(target),true,'far/adjacent trusted tap selected a different target')
      assertSavedState(await savedState(page),panBefore,'selection alone edited the board')
      let clues
      if(game==='nonogram')clues=await mappedClues(page,recipe,index)
      if(!['shikaku','hashi'].includes(game))await enterCell(page,engine,recipe,index)
      positions.push([index,Math.round(point.width),Math.round(point.height),...(clues?[clues]:[])])
      if(game==='nonogram'&&index===far){await exposeAction(page,engine,target);await mappedClues(page,recipe,index);await capture()}
    }
    if(game==='hashi'){
      // Pair/count controls are already covered by the ordinary journey and solved
      // recipe; this observation binds distant island hit selection and clue size.
      assert.ok((await actionPoint(page,board.locator(`[data-island="${far}"]`))).font>=12,'distant island clue is unreadable')
    }
    const finalScroll=await frame.evaluate(n=>[n.scrollLeft,n.scrollTop])
    assert.ok(finalScroll.some((v,i)=>Math.abs(v-startScroll[i])>8),'large-board observation did not actually pan into distant content')
    return {status:engine==='chromium'?'passed':'partial',pan:engine==='chromium'?'trusted-touch':'wheel-positioned taps',observed:digest([recipe.publication,positions,header]),count:positions.length,scroll:finalScroll.map(Math.round),noPanEntry:true}
  } catch(error){failed=true;throw error} finally {
    if(game==='shikaku'&&!wasMoving)await tapAction(page,engine,move)
    await tapAction(page,engine,fit)
    if(!failed)assertSavedState(await savedState(page),before,'Fit/pan/selection changed retained entries')
  }
}

async function controlGeometry(page) {
  return page.locator('.number-pad button:visible,.value-controls button:visible,.input-tools button:visible,.rectangle-controls button:visible,.player-actions button:visible').evaluateAll(nodes=>nodes.map(n=>{const r=n.getBoundingClientRect();return {label:n.textContent.trim().slice(0,60),width:Math.round(r.width),height:Math.round(r.height),x:Math.round(r.x),y:Math.round(r.y)}}))
}

export async function responsiveScenario(page,game,engine,readiness,capture) {
  const recipe=await admittedRecipe(page,game,readiness),original=await savedState(page),viewport=page.viewportSize(),settings=page.locator('.play-settings'),notes=notesButton(page),wasOpen=await settings.getAttribute('open')!==null,hadNotes=await notes.count()?await notes.getAttribute('aria-pressed')==='true':false
  const snapshot=activeProgress(original,new URL(page.url()).pathname).snapshot,entries=snapshot.entries||snapshot.values
  requireProof(Array.isArray(entries),'saved resize entry schema unavailable')
  const candidates=await cells(page).all();let cell
  for(const c of candidates)if([0,'unknown','blank'].includes(entries[Number(await c.getAttribute('data-cell'))])){cell=c;break}
  requireProof(cell,'representative empty resize cell unavailable')
  const index=Number(await cell.getAttribute('data-cell')),states=[]
  let added=false
  try {
    await tapAction(page,engine,cell)
    if(await notes.count()){
      if(!hadNotes)await tapAction(page,engine,notes)
      await tapAction(page,engine,digit(page,1));added=true
    }
    const progress=await savedState(page)
    for(const [width,height,open,text]of [[390,844,true,false],[768,1024,true,false],[1024,768,false,false],[1440,1000,true,false],[844,390,false,false],[844,390,true,true],[360,640,false,true],[320,568,true,false],[390,844,false,false]]){
      await page.setViewportSize({width,height});await settleViewport(page)
      if((await settings.getAttribute('open')!==null)!==open)await tapAction(page,engine,settings.locator('summary'))
      const check=async()=>{
        await tapAction(page,engine,page.getByRole('button',{name:'Zoom in',exact:true}))
        await tapAction(page,engine,page.getByRole('button',{name:'Fit board',exact:true}))
        const metrics=await playMetrics(page)
        assert.ok(metrics.documentWidth<=width+1&&metrics.board.width>0&&metrics.board.x>=-1&&metrics.board.x+metrics.board.width<=width+1,'resize/Fit left stale board width or document overflow')
        assert.ok(metrics.board.x>=metrics.frame.x-1&&metrics.board.x+metrics.board.width<=metrics.frame.x+metrics.frame.width+1,'Fit left the board clipped inside its frame')
        assert.equal(await currentSelected(cell),true,'resize/Fit lost the intended selection')
        assertSavedState(await savedState(page),progress,'resize/settings/text presentation changed progress')
        assert.equal(await page.evaluate(()=>visualViewport.scale),1,'resize changed visual viewport scale')
        if([320,360,390,844].includes(width)){
          if(await notes.count()&&await notes.getAttribute('aria-pressed')==='true')await tapAction(page,engine,notes)
          await enterCell(page,engine,recipe,index)
          if(await notes.count()&&await notes.getAttribute('aria-pressed')!=='true')await tapAction(page,engine,notes)
        }
        if(width===844){
          await exposeAction(page,engine,page.locator('.player-board-frame:visible').first())
          const live=await playMetrics(page)
          assert.ok(live.board.y>=-1&&live.board.y+live.board.height<=height+1&&live.controls.every(c=>c.y>=-1&&c.y+c.height<=height+1),'landscape board and editing controls are not visible together')
          if(open)await capture()
        }
        const controls=await controlGeometry(page)
        assert.ok(controls.length&&controls.every(c=>c.width>=24&&c.height>=24&&c.x>=-1&&c.x+c.width<=width+1),'responsive keypad/history target is too small or clipped')
        states.push([width,height,open,text,digest(controls),await headerWitness(page)])
      }
      if(text)await scaledText(page,check);else await check()
    }
    let pinch
    if(engine==='chromium'&&game==='sudoku')try{pinch=await intentionalPinch(page)}catch(error){if(!(error instanceof BrowserCapabilityError))throw error;pinch={status:'blocked',gap:error.message}}
    return {observed:digest([recipe.publication,states]),transitions:states.length,text:'root 125%; not native',fitWithoutReload:true,landscapeEntryUndo:true,notes:added,...(pinch?{pinch,status:pinch.status?'partial':'passed'}:{})}
  } finally {
    if(added){await tapAction(page,engine,page.getByRole('button',{name:'Undo',exact:true}));assertSavedState(await savedState(page),original,'resize case did not restore its owned note')}
    if(await notes.count()&&(await notes.getAttribute('aria-pressed')==='true')!==hadNotes)await tapAction(page,engine,notes)
    if((await settings.getAttribute('open')!==null)!==wasOpen)await tapAction(page,engine,settings.locator('summary'))
    await page.setViewportSize(viewport);await settleViewport(page)
  }
}

export async function chooserObservations(page,dialog,engine,capture,touch=true) {
  const tap=async locator=>{await exposeAction(page,touch?engine:'desktop',locator);return traceInput(page,()=>activate(page,locator,touch),touch)}
  const originalViewport=page.viewportSize(),results=[],selectedNames=await dialog.locator('button[aria-pressed="true"]').allTextContents()
  let minimum=Infinity
  // Real computed colors, including alpha/ancestor compositing. Gradients/images
  // are outside this fixed numeric observer and must not become a false pass.
  const contrast=()=>dialog.getByRole('button').evaluateAll(nodes=>{
    const luminance=c=>c.map(v=>{v/=255;return v<=.04045?v/12.92:((v+.055)/1.055)**2.4}).reduce((n,v,i)=>n+v*[.2126,.7152,.0722][i],0)
    const canvas=new OffscreenCanvas(1,1),ctx=canvas.getContext('2d',{willReadFrequently:true}),rows=[]
    for(const node of nodes){const r=node.getBoundingClientRect();if(!r.width||!r.height)continue;const parents=[];for(let p=node;p;p=p.parentElement)parents.unshift(p);ctx.globalAlpha=1;ctx.fillStyle='#fff';ctx.fillRect(0,0,1,1);let alpha=1
      for(const p of parents){const s=getComputedStyle(p);if(s.backgroundImage!=='none')return {unsupported:true};alpha*=Number(s.opacity);ctx.globalAlpha=alpha;ctx.fillStyle=s.backgroundColor;ctx.fillRect(0,0,1,1)}
      const bg=[...ctx.getImageData(0,0,1,1).data].slice(0,3);ctx.globalAlpha=alpha;ctx.fillStyle=getComputedStyle(node).color;ctx.fillRect(0,0,1,1);const fg=[...ctx.getImageData(0,0,1,1).data].slice(0,3),a=luminance(bg),b=luminance(fg)
      rows.push({ratio:(Math.max(a,b)+.05)/(Math.min(a,b)+.05),disabled:node.disabled||node.getAttribute('aria-disabled')==='true',selected:node.getAttribute('aria-pressed')==='true',width:r.width,height:r.height,label:node.textContent.trim()})
    }return {rows}
  })
  try {
    for(const scheme of ['dark','light']){
      await page.emulateMedia({colorScheme:scheme})
      for(const [width,height]of [[360,640],[844,390]]){
        await page.setViewportSize({width,height});await settleViewport(page)
        const run=async()=>{
          const selected=dialog.locator('button[aria-pressed="true"]').first()
          requireProof(await selected.count()===1,'chooser selected state unavailable')
          await tap(selected)
          const states=[]
          for(const mode of ['selected','hover','keyboard-focus']){
            if(mode==='hover'){const p=await actionPoint(page,selected);await page.mouse.move(p.x,p.y)}
            if(mode==='keyboard-focus'){if(touch)await tap(selected);else await page.keyboard.press('Tab')}
            const values=await contrast();requireProof(!values.unsupported,'chooser contrast background image/gradient unsupported')
            const enabled=values.rows.filter(r=>!r.disabled)
            assert.ok(enabled.length&&enabled.every(r=>r.ratio>=4.5),'enabled chooser labels fail rendered 4.5:1 contrast')
            minimum=Math.min(minimum,...enabled.map(r=>r.ratio))
            assert.ok(enabled.every(r=>r.width>=24&&r.height>=24),'chooser has undersized enabled targets')
            states.push([touch&&mode==='keyboard-focus'?'touch-focus':mode,Math.round(Math.min(...enabled.map(r=>r.ratio))*100)/100,values.rows.filter(r=>r.selected).length,values.rows.filter(r=>r.disabled).length])
          }
          const more=dialog.locator('details.setup-more')
          await tap(more.locator('summary'))
          const options=more.getByRole('button').all();for(const option of (await options).slice(-2)){await exposeAction(page,touch?engine:'desktop',option);if(await option.isEnabled())await tap(option)}
          for(const name of selectedNames){const original=dialog.getByRole('button',{name:name.trim(),exact:true});requireProof(await original.count()===1,'chooser original choice is ambiguous');if(await original.getAttribute('aria-pressed')!=='true')await tap(original)}
          await tap(more.locator('summary'))
          const play=dialog.getByRole('button',{name:/^Play /}).first(),cancel=dialog.getByRole('button',{name:'Cancel',exact:true})
          await exposeAction(page,touch?engine:'desktop',play);assert.ok((await actionPoint(page,cancel)).hit,'short chooser hides Cancel')
          results.push([scheme,width,height,digest(states)])
          await capture(scheme,width)
        }
        if(scheme==='light'&&width===844)await scaledText(page,run);else await run()
      }
    }
    return {states:digest(results),views:results.length,minContrast:Math.round(minimum*100)/100}
  }finally{await page.setViewportSize(originalViewport);await page.emulateMedia({colorScheme:'light'});await settleViewport(page)}
}

async function requestCheckedHint(page,game,engine) {
  const button=page.getByRole('button',{name:'A little hint',exact:true}),code=new URL(page.url()).pathname.split('/').at(-1)
  await exposeAction(page,engine,button)
  const pending=page.waitForResponse(response=>{
    if(new URL(response.url()).pathname!=='/api/games/'+game||response.request().method()!=='POST')return false
    try {const q=response.request().postDataJSON();return q.kind==='hint'&&q.identity?.id.split(':').at(-1)===code}catch{return false}
  })
  const [response]=await Promise.all([pending,tapAction(page,engine,button)])
  requireProof(response.status()===200,'normal checked hint response unavailable')
  const body=await response.body();requireProof(body.length<=1024*1024,'checked hint exceeds its existing UI envelope')
  const envelope=JSON.parse(body),request=response.request().postDataJSON()
  requireProof(envelope.status==='success'&&digest(envelope.request)===digest(request),'checked hint request/identity binding unavailable')
  const hint=envelope.result
  requireProof(hint?.status==='hint','fixed long-hint sample did not produce a checked hint')
  return {hint,sha:digest(envelope)}
}

function cellReference(label,columns) {
  const match=/\brow\s+(\d+),?\s+column\s+(\d+)/i.exec(label)
  return match?{index:(Number(match[1])-1)*columns+Number(match[2])-1,row:Number(match[1]),column:Number(match[2])}:null
}

async function revealHint(page,engine) {
  const reveal=page.getByRole('button',{name:'Show the next move',exact:true})
  requireProof(await reveal.count()===1,'actionable hint reveal control unavailable')
  await tapAction(page,engine,reveal)
}

export async function longHintScenario(page,game,engine,readiness,capture) {
  const recipe=await admittedRecipe(page,game,readiness),panel=page.locator('.checked-hint')
  let checked=await requestCheckedHint(page,game,engine),setupNotes=false
  // Kakuro chains can initially have no applicable notes. Prepare one actually
  // checked removal through normal Notes UI, then request a fresh checked hint.
  if(game==='kakuro'&&checked.hint.step?.rule!=='forced-cell'){
    const step=checked.hint.steps?.find(s=>Number.isInteger(s.index)&&Number.isInteger(s.before)&&Number.isInteger(s.after)&&(s.before&~s.after))
    requireProof(step,'long Kakuro sample needs a bounded actionable checked removal')
    const value=Array.from({length:9},(_,i)=>i+1).find(v=>(step.before&~step.after)&(1<<v))
    await tapAction(page,engine,notesButton(page));await tapAction(page,engine,page.locator(`.player-board-frame [data-cell="${step.index}"]`));await tapAction(page,engine,digit(page,value));await tapAction(page,engine,notesButton(page));setupNotes=true
    checked=await requestCheckedHint(page,game,engine)
  }
  await revealHint(page,engine)
  const apply=panel.getByRole('button',{name:/^Apply \d+ changes?$/}),dismiss=panel.getByRole('button',{name:/^Dismiss(?: hint)?$/})
  requireProof(await apply.count()===1&&await dismiss.count()===1,'long actionable Apply/dismiss contract unavailable')
  const links=panel.locator('button,a'),locations=(await links.allTextContents()).flatMap((label,i)=>/^\s*row\s+\d+,?\s+column\s+\d+/i.test(label)?[{i,...cellReference(label,recipe.columns)}]:[])
  const unique=[...new Set(locations.map(l=>l.index))]
  requireProof(unique.length>=12&&unique.length<=recipe.rows*recipe.columns,'sample is not a long supporting-location hint')
  requireProof(locations.every(l=>l.index>=0&&l.index<recipe.rows*recipe.columns),'hint location lies outside the admitted board')
  if(game==='nonogram'){
    const effects=checked.hint.step?.effects
    requireProof(recipe.n===25&&effects?.length===25&&effects.every(e=>e.value==='filled'&&Math.floor(e.cell/25)===Math.floor(effects[0].cell/25)),'sample is not an actual whole-row 25-change Nonogram hint')
    assert.ok(effects.every(e=>unique.includes(e.cell)),'whole-row hint does not expose every affected supporting location')
  }else requireProof(Array.isArray(checked.hint.steps)&&checked.hint.steps.length>1,'sample lacks a long checked chain')
  const leading=await panel.locator('.hint-explanation').evaluate(node=>{
    const children=[...node.children].filter(n=>n.getClientRects().length&&getComputedStyle(n).display!=='none')
    const first=children.find(n=>n.matches('p,h3,[role="status"],.hint-summary'))
    return first?.innerText?.trim()||''
  })
  assert.ok(leading.length>0&&leading.length<=220&&/shade|fill|enter|remove|mark/i.test(leading)&&!/^(one undo|supporting location)/i.test(leading)&&((leading.match(/row\s+\d+,?\s+column/gi)||[]).length<=3),'long hint lacks a concise leading human instruction')
  if(game==='nonogram')assert.match(leading,new RegExp('row\\s+'+(Math.floor(checked.hint.step.effects[0].cell/25)+1)+'\\b','i'),'grouped instruction names a different row')
  assert.ok((await actionPoint(page,apply)).hit&&(await actionPoint(page,dismiss)).hit,'long hint Apply/dismiss are outside the actual viewport or covered')
  await capture('hint')
  const before=await savedState(page),visible=await renderedState(page),visited=[]
  await tapAction(page,engine,apply)
  const edited=await savedState(page)
  assert.notEqual(digest(edited),digest(before),'long-hint Apply did not change actual saved state')
  if(game==='nonogram'){
    const expected=structuredClone(before),row=activeProgress(expected,new URL(page.url()).pathname)
    for(const e of checked.hint.step.effects)row.snapshot.entries[e.cell]=e.value
    assertSavedState(edited,expected,'whole-row Apply changed the wrong cells')
  }else{
    const expected=structuredClone(before),snapshot=activeProgress(expected,new URL(page.url()).pathname).snapshot
    const hint=checked.hint,place=game==='calcudoku'?hint.action==='place':hint.step?.rule==='forced-cell'
    if(place){
      const step=game==='calcudoku'?hint:hint.step
      requireProof(Number.isInteger(step.index)&&Number.isInteger(step.value)&&step.index>=0&&step.index<recipe.rows*recipe.columns,'checked hint placement unavailable')
      snapshot[game==='kakuro'?'values':'entries'][step.index]=step.value;snapshot.notes[step.index]=[]
    }else if(game==='calcudoku'){
      requireProof(hint.action==='eliminate'&&Number.isInteger(hint.index)&&Array.isArray(snapshot.notes[hint.index]),'checked note removal unavailable')
      snapshot.notes[hint.index]=snapshot.notes[hint.index].filter(v=>v!==hint.value)
    }else for(const step of hint.steps){
      requireProof(Number.isInteger(step.index)&&Array.isArray(snapshot.notes[step.index])&&Number.isInteger(step.before)&&Number.isInteger(step.after),'checked Kakuro removal unavailable')
      snapshot.notes[step.index]=snapshot.notes[step.index].filter(v=>!((step.before&~step.after)&(1<<v)))
    }
    requireProof(digest(expected)!==digest(before),'long checked hint has no applicable UI effect in this sample')
    assertSavedState(edited,expected,'long-hint Apply differs from the checked placement/removals')
  }
  await tapAction(page,engine,page.getByRole('button',{name:'Undo',exact:true}))
  assertSavedState(await savedState(page),before,'one Undo did not restore the complete long hint step')
  assertRenderedState(await renderedState(page),visible,'Undo did not restore visible long-hint state')
  const refreshed=await requestCheckedHint(page,game,engine);await revealHint(page,engine)
  requireProof(digest(refreshed.hint)===digest(checked.hint),'restored board produced a different checked hint; fixed supporting locations unavailable')
  for(const location of locations){
    const link=links.nth(location.i)
    for(let depth=0;depth<4;depth++){
      const closed=link.locator('xpath=ancestor::details[not(@open)]')
      if(!await closed.count())break
      await tapAction(page,engine,closed.last().locator('summary').first())
    }
    await tapAction(page,engine,link)
    const target=page.locator(`.player-board-frame [data-cell="${location.index}"]`)
    await exposeAction(page,engine,target)
    assert.equal(await currentSelected(target),true,'supporting location link focused the wrong admitted cell')
    assertSavedState(await savedState(page),before,'supporting location operation edited progress')
    assertRenderedState(await renderedState(page),visible,'supporting location changed persistent board content')
    visited.push([location.index,Math.round((await actionPoint(page,target)).width)])
  }
  const proof=panel.locator('details').filter({has:page.locator('summary').filter({hasText:/checked|proof|evidence/i})}).first()
  requireProof(await proof.count()===1,'expandable checked evidence unavailable')
  if(await proof.getAttribute('open')===null)await tapAction(page,engine,proof.locator('summary').first())
  assert.ok((await proof.innerText()).trim().length>30,'expanded checked evidence is empty')
  assertSavedState(await savedState(page),before,'opening proof changed entries/notes')
  await tapAction(page,engine,dismiss)
  assert.equal(await apply.count(),0,'dismissed hint remains actionable')
  assertSavedState(await savedState(page),before,'Dismiss changed progress')
  return {observed:digest([checked.sha,visited,leading]),locations:visited.length,applyUndo:true,dismiss:true,setupNotes,...(engine==='webkit'?{positioning:'wheel; native taps, not touch drag'}:{})}
}

export async function hintRequestRecovery(page,game,engine) {
  const button=page.getByRole('button',{name:'A little hint',exact:true}),pattern='**/api/games/'+game
  let aborted=false
  const route=async route=>{
    let request
    try {request=route.request().postDataJSON()}catch{}
    if(!aborted&&request?.kind==='hint'){aborted=true;return route.abort('failed')}
    return route.fallback()
  }
  const before=await savedState(page)
  await page.route(pattern,route)
  try {
    await tapAction(page,engine,button)
    await expectState(page,()=>document.body.innerText.match(/unavailable|try again|could not|failed|unable|retry/i),undefined,'hint transport failure has no explicit recovery message')
    assertSavedState(await savedState(page),before,'failed hint request changed board progress')
  }finally{await page.unroute(pattern,route)}
  requireProof(aborted,'owned one-request hint abort was not exercised')
  await requestCheckedHint(page,game,engine)
  assertSavedState(await savedState(page),before,'recovered hint request changed progress')
  return {setup:'one owned hint request aborted; no mocked response',recovered:true}
}

async function digitGestureScenario(page,engine) {
  const settings=page.locator('.play-settings'),wasOpen=await settings.getAttribute('open')!==null
  if(!wasOpen)await tapAction(page,engine,settings.locator('summary'))
  const highlight=page.getByRole('checkbox',{name:'Highlight same number',exact:true})
  requireProof(await highlight.count()===1,'hold-highlight preference contract unavailable')
  const checked=await highlight.isChecked()
  if(!checked)await tapAction(page,engine,highlight)
  const control=digit(page,1),before=await savedState(page),cdp=await page.context().newCDPSession(page)
  let active=false
  const send=(type,points)=>cdp.send('Input.dispatchTouchEvent',{type,touchPoints:points})
  try {
    for(const kind of ['hold','drag-away','cancel']){
      const p=await exposeAction(page,engine,control),point={id:1,x:p.x,y:p.y}
      await send('touchStart',[point]);active=true
      if(kind==='hold')await expectState(page,()=>[...document.querySelectorAll('.number-pad button,.value-controls button')].some(n=>n.classList.contains('inspecting')&&n.textContent.trim()==='1'),undefined,'trusted hold did not expose inspection highlight')
      if(kind==='drag-away')await send('touchMove',[{...point,x:Math.max(4,point.x-60),y:Math.max(4,point.y-60)}])
      await send(kind==='cancel'?'touchCancel':'touchEnd',[]);active=false
      await settleViewport(page)
      assertSavedState(await savedState(page),before,kind+' unexpectedly entered or removed a note')
      if(kind==='hold'){
        const unlock=page.getByRole('button',{name:'Unlock highlight 1',exact:true})
        assert.equal(await unlock.count(),1,'touch hold did not preserve intentional highlight lock')
        await tapAction(page,engine,unlock)
      }
    }
    return {holdUnlock:true,dragAway:true,cancel:true,noEdit:true}
  } finally {
    try{if(active)await send('touchCancel',[])}finally{await cdp.detach()}
    if(!checked)await tapAction(page,engine,highlight)
    if(!wasOpen)await tapAction(page,engine,settings.locator('summary'))
  }
}

async function headerWitness(page) {
  const title=page.locator('.play-heading h1').first()
  requireProof(await title.count()===1,'current puzzle heading unavailable')
  const metrics=await title.evaluate(node=>{
    const text=node.textContent.trim(),rect=node.getBoundingClientRect(),tops=[]
    const walker=document.createTreeWalker(node,NodeFilter.SHOW_TEXT);let t
    while(t=walker.nextNode())if(t.textContent.trim()){const range=document.createRange();range.selectNodeContents(t);for(const r of range.getClientRects())if(r.width&&r.height)tops.push(Math.round(r.top))}
    const context=[...node.closest('.play-heading').querySelectorAll('button,[role="status"],.puzzle-meta,.play-progress,.progress')].filter(n=>n.getClientRects().length).map(n=>{const r=n.getBoundingClientRect();return {text:n.textContent.trim().slice(0,160),left:r.left,right:r.right}})
    return {text,width:Math.round(rect.width),right:rect.right,left:rect.left,wordLines:new Set(tops).size,viewport:innerWidth,context}
  })
  if(!/\s/.test(metrics.text))assert.equal(metrics.wordLines,1,'puzzle heading splits a word across lines')
  assert.ok(metrics.left>=-1&&metrics.right<=metrics.viewport+1,'puzzle title overflows its viewport')
  assert.ok(metrics.context.every(n=>n.left>=-1&&n.right<=metrics.viewport+1),'puzzle header status/action overflows its viewport')
  return digest(metrics)
}
