import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import { activate, savedState, assertSavedState, renderedState, assertRenderedState } from './browser_contract.mjs'

// Fixed observations only. Neither a candidate nor task data selects scripts,
// URLs, filenames or commands. These images describe actual viewport states;
// they do not replace the independent image judgment or interaction obligations.
export async function captureViewport(receipt, output, page, engine, device, game, state = '') {
  const name = `${engine}-${device}-${game.toLowerCase()}${state ? '-' + state : ''}-viewport.png`
  assert.match(name,/^(chromium|webkit)-(desktop|mobile)-(sudoku|suguru|nonogram|binary|futoshiki|shikaku|hashi|calcudoku|kakuro)(-(chooser-dark|initial|largest-light|landscape|tablet-phone|hint|preferences|notes|commit|privacy|hint-conflict|solved))?-viewport\.png$/)
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
  const {viewport,...position}=observed // actual dimensions are bound once in viewportImages
  receipt.experienceObservations.push({image_index:receipt.viewportImages.length-1,...(!known?{path:route}:{}),...position})
  return name
}

export const settleViewport = page => page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))))

export class BrowserCapabilityError extends Error { name = 'BrowserCapabilityError' }
const requireProof = (condition, message) => { if (!condition) throw new BrowserCapabilityError(message) }
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

export async function preferenceScenario(page, readiness) {
  const settings=page.locator('.play-settings')
  requireProof(await settings.count()===1,'settings contract unavailable')
  if(await settings.getAttribute('open')===null)await activate(page,settings.locator('summary'),true,false)
  requireProof(await page.getByRole('button',{name:'Number first',exact:true}).count()===1,'entry-order contract unavailable')
  const original=await checks(page), mode=await page.getByRole('button',{name:'Number first',exact:true}).getAttribute('aria-pressed')==='true'?'Number first':'Cell first'
  const auto=page.getByRole('checkbox',{name:'Auto-update notes',exact:true})
  requireProof(await auto.count()===1,'persisted assistance checkbox unavailable')
  const beforeBoard=await savedState(page), phases=[]
  for(const value of [false,true]){
    if(await auto.isChecked()!==value)await activate(page,auto,true,false)
    const expected=await checks(page)
    for(const name of ['Number first','Cell first']){
      const input=await traceInput(page,()=>activate(page,page.getByRole('button',{name,exact:true}),true,false))
      await settleViewport(page)
      assert.deepEqual(await checks(page),expected,'entry-order activation changed unrelated preferences')
      await reloadBoard(page,readiness)
      if(await settings.getAttribute('open')===null)await activate(page,settings.locator('summary'),true,false)
      assert.equal(await page.getByRole('button',{name,exact:true}).getAttribute('aria-pressed'),'true','entry order did not persist across reload')
      assert.deepEqual(await checks(page),expected,'unrelated preference changed after reload')
      phases.push({mode:name,autoNotes:value,checks:digest(expected),trusted:input.downs})
    }
  }
  // Restore only values observed in this owned browser profile, using its UI.
  for(const row of original){const control=page.getByRole('checkbox',{name:row.label,exact:true});if(await control.isChecked()!==row.checked)await activate(page,control,true,false)}
  await activate(page,page.getByRole('button',{name:mode,exact:true}),true,false)
  assertSavedState(await savedState(page),beforeBoard,'preferences changed board progress')
  return {phases:phases.map(({checks,...row})=>row),preferenceStates:[...new Set(phases.map(row=>row.checks))],boardUnchanged:true}
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
  return {status:engine==='chromium'?'passed':'partial',input:{touches:input.downs,contacts:input.maxContacts},notes:[1,2],undo:undone,saved:digest(edited),history:true,reloaded:true,...(engine==='webkit'?{gap:'trusted simultaneous-contact API unavailable; sequential taps only'}:{})}
}

export async function shikakuScenario(page, readiness) {
  const board=page.locator('.shikaku-board'), data=await board.locator('[data-cell]').allTextContents(), n=Math.sqrt(data.length)
  requireProof(Number.isInteger(n)&&n<=10,'fresh small Shikaku geometry unavailable')
  const clues=data.map(t=>/^\s*\d+\s*$/.test(t)?Number(t):0), singleton=clues.indexOf(1)
  let rectangle
  for(let a=0;a<data.length&&!rectangle;a++)for(let b=a;b<data.length;b++){
    if(a%n>b%n)continue
    const indexes=[]
    for(let r=Math.floor(a/n);r<=Math.floor(b/n);r++)for(let c=a%n;c<=b%n;c++)indexes.push(r*n+c)
    const numbered=indexes.filter(i=>clues[i]>0)
    if(indexes.length>1&&numbered.length===1&&clues[numbered[0]]===indexes.length){rectangle={a,b,indexes};break}
  }
  requireProof(singleton>=0&&rectangle,'catalogue sample lacks singleton/rectangle witnesses')
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
    witnesses.push({cells:indexes.length,saved:digest(saved),input:{touches:input.downs,contacts:input.maxContacts}})
    if(indexes.length===1){
      // Reload intentionally loses in-tab history. Restart through the actual UI.
      await activate(page,page.getByRole('button',{name:'Restart',exact:true}),true,false)
      const confirm=page.getByRole('dialog').getByRole('button',{name:'Restart',exact:true})
      await activate(page,confirm,true,false)
      await reloadBoard(page,readiness)
      assert.deepEqual(await painted(),[],'restart did not produce a fresh rectangle board')
      assertSavedState(await savedState(page),baseline,'restart changed the fresh snapshot')
    }
  }
  return {witnesses,drag:'existing separate drag/zoom contract retained'}
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

export async function hintContinuityScenario(page, readiness, unrelatedPath, capture) {
  requireProof(/^\/binary\/[A-Za-z0-9_-]+$/.test(unrelatedPath),'admitted unrelated board path unavailable')
  const panel=page.locator('.checked-hint'), reveal=page.getByRole('button',{name:'Show the next move',exact:true})
  requireProof(await reveal.count()===1,'revealed Nonogram hint contract unavailable')
  await activate(page,reveal,true,false)
  const apply=()=>panel.getByRole('button',{name:/^Apply \d+ changes?$/})
  requireProof(await apply().count()===1,'actionable revealed hint unavailable')
  const before=(await savedState(page))
  requireProof(before.length===1,'hint scenario requires one fresh owned board')
  const key=before[0].key, hint=digest(await panel.innerText()), url=page.url()
  const other=await page.context().newPage()
  other.setDefaultTimeout(page.getDefaultTimeout?.()||20000)
  let denied=false
  try {
    readiness.watch(other,new URL(url).origin)
    await readiness.board(other,new URL(unrelatedPath,url).href)
    const unrelatedBefore=await savedState(other)
    const otherKey=unrelatedBefore.find(row=>row.key!==key)?.key
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
    assert.deepEqual((await savedState(page)).find(r=>r.key===key),before[0],'unrelated tab changed current board storage')
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
    return {unrelated:{nativeStorageEvent:true,hintPreserved:true,board:digest(before[0].snapshot)},conflict:{setup:'controlled page-local write denial',nativeStorageEvent:true,localPreserved:true,remotePreserved:true,staleApplyAbsent:true,local:digest(local),remote:digest(remote)}}
  } finally {
    if(denied&&!page.isClosed())await page.evaluate(()=>window.runnerScenarioDenied?.restore())
    await other.close()
  }
}

// Observe the admission response already consumed by the candidate UI. The
// native-image gate owns answer validity; this lane owns actual UI completion.
// No direct API call, solver, repository script or state injection is used.
export async function completionRecipe(page, game, readiness) {
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
  requireProof(Number.isInteger(n)&&n>=4&&n<=9&&(!definition.columns||Array.isArray(definition.columns)||definition.columns===n),'representative completion requires a bounded square board')
  const answer=transfer.answer
  if(game==='shikaku')requireProof(answer.length>0&&answer.length<=n*n&&answer.every(r=>r&&[r.top,r.left,r.bottom,r.right].every(Number.isInteger)&&r.top>=0&&r.left>=0&&r.bottom>=r.top&&r.right>=r.left&&r.bottom<n&&r.right<n),'completion rectangle recipe unavailable')
  else if(game==='hashi')requireProof(Array.isArray(definition.islands)&&definition.islands.length<=16&&answer.length<=32&&answer.every(b=>b&&[b.a,b.b,b.count].every(Number.isInteger)&&b.a>=0&&b.a<b.b&&b.b<definition.islands.length&&b.count>=0&&b.count<=2),'completion bridge recipe unavailable')
  else requireProof(answer.length===n*n&&answer.every(v=>game==='nonogram'?['filled','empty'].includes(v):game==='binary'?['zero','one'].includes(v):Number.isInteger(v)&&v>=(game==='kakuro'?0:1)&&v<=9),'completion cell recipe unavailable')
  return {game,n,identity:{game,id:identity.id,contentRevision:identity.contentRevision},definition,answer,publication:digest(transfer)}
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
  return {identity:recipe.identity.id,publication:recipe.publication,clicks:input.downs,solved:true,reloaded:true,saved:digest(saved)}
}
