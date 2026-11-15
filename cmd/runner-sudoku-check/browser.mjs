import assert from 'node:assert/strict';
import {readFile,writeFile} from 'node:fs/promises';
import {createHash} from 'node:crypto';
import path from 'node:path';
import {pathToFileURL} from 'node:url';
import {viewports,assertGeometry,assertNeighborGeometry,measure} from './geometry.mjs';
import {neighborSelectors,stableBoardGeometry,boundedFailure,reopenSudoku,afterReopen} from './browser-contract.mjs';

const options=JSON.parse(await readFile(process.argv[2],'utf8'));
const {chromium,webkit}=await import(pathToFileURL(options.playwright).href);
const hash=value=>createHash('sha256').update(typeof value==='string'||Buffer.isBuffer(value)?value:JSON.stringify(value)).digest('hex');
const report={schema:1,status:'failed',complete:false,browser_closed:false,engines:[],prerequisites:[],unrun:[],geometry:[],journeys:[],neighbors:[],screenshots:[],errors:[],document_failures:[],limits:[
  'Viewport/device input is headless emulation, not physical Duo/iPad/iPhone or native Safari.',
  'Chromium hold/cancel uses trusted CDP touch; WebKit exposes trusted taps but no public hold/cancel touch API, so its hold/move cancellation uses a trusted mouse pointer and Options pinning.',
  'Secondary offscreen actions use browser scrolling before trusted clicks/taps; primary touch geometry is checked without automatic scrolling.',
  'Metrics do not judge branding, hierarchy, icon style, spacing or visual quality. Independent full viewport image review is mandatory.',
],input:{touch:0,mouse:0,keyboard:0},input_scope:'Sampled trusted event counters from the explicit entry/keyboard journeys; document navigation resets page counters.',viewport_contract:viewports};
report.playwright_version=JSON.parse(await readFile(path.join(path.dirname(options.playwright),'package.json'),'utf8')).version;
let activeBrowser,current,stopping=false,imageBytes=0,profile=0;
const functionalJourneys=['entry-pin-cancel-history','touch-in-progress-resize','desktop-in-progress-resize','side-swap-persistence','hint-explanation-apply-undo','completion-new-resume','touch-completion-new-resume','desktop-keyboard-focus','appearance-controls'];
const save=()=>writeFile(path.join(options.evidence,'browser.json'),JSON.stringify(report,null,2)+'\n',{mode:0o600});
const pauseForHold=()=>new Promise(resolve=>setTimeout(resolve,900)); // Actual gesture duration exceeds the app's 750ms touch threshold; never a readiness sleep.
const bounded=boundedFailure;
const failures=()=>[report.journeys,report.neighbors,report.geometry,report.prerequisites].reduce((n,rows)=>n+rows.filter(row=>row.status!=='passed').length,0)+report.errors.length;

async function check(engine,id,action,{rows=report.journeys,failureStatus='failed'}={}) {
  const row={engine,id,status:'failed'};
  rows.push(row);
  try {if(stopping)throw new Error('checker cancellation requested');row.proof=await action();row.status='passed';}
  catch(error){row.status=failureStatus;row.failure=bounded(error);if(current&&!current.isClosed())try{await capture(current,engine,id+'-failure')}catch{}}
  await save();
  return row.status==='passed';
}

async function settle(page,boardSelector='.sudoku-play .puzzle-board') {
  await page.evaluate(async()=>{
    await document.fonts.ready;
    await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
  });
  await page.waitForFunction(stableBoardGeometry,boardSelector,{timeout:5000,polling:'raf'});
}

async function capture(page,engine,state) {
  const id=state.replace(/[^a-z0-9-]/g,'-').slice(0,90),name=`${engine}-${id}.png`;
  if(report.screenshots.some(row=>row.name===name))return;
  assert.ok(report.screenshots.length<80,'fixed viewport image count exceeded');
  const appearance=await palette(page);
  if(state.endsWith('-dark'))assert.ok(appearance.choice==='dark'||appearance.choice==='system'&&appearance.systemDark,'dark capture is not using an actual dark palette');
  if(state.endsWith('-light'))assert.ok(appearance.choice==='light'||appearance.choice==='system'&&!appearance.systemDark,'light capture is not using an actual light palette');
  const data=await page.screenshot({fullPage:false,animations:'disabled',caret:'hide'});
  assert.ok(data.length<=2<<20&&imageBytes+data.length<=10<<20,'viewport image evidence exceeds its fixed byte bound');
  imageBytes+=data.length;
  await writeFile(path.join(options.evidence,name),data,{flag:'wx',mode:0o600});
  report.screenshots.push({name,sha256:hash(data),bytes:data.length,...page.viewportSize(),path:new URL(page.url()).pathname,state,full_viewport:true,appearance});
}

async function palette(page) {
  return page.evaluate(()=>({choice:document.documentElement.dataset.appearance,systemDark:matchMedia('(prefers-color-scheme: dark)').matches,page:getComputedStyle(document.documentElement).getPropertyValue('--puzzle-page').trim()}));
}

async function contextFor(browser,viewport,scheme='light') {
  assert.ok(++profile<=64,'fixed isolated browser profile count exceeded');
  const context=await browser.newContext({viewport:{width:viewport.width,height:viewport.height},isMobile:viewport.touch,hasTouch:viewport.touch,
    colorScheme:scheme,reducedMotion:'reduce',ignoreHTTPSErrors:true,extraHTTPHeaders:{'X-Runner-Sudoku-Profile':String(profile)}});
  await context.route('**/*',route=>new URL(route.request().url()).origin===options.origin?route.continue():route.abort());
  await context.addInitScript(()=>{
    window.runnerSudokuInput={touch:0,mouse:0,keyboard:0};
    document.addEventListener('pointerdown',event=>{if(event.isTrusted)window.runnerSudokuInput[event.pointerType==='touch'?'touch':'mouse']++},true);
    document.addEventListener('keydown',event=>{if(event.isTrusted)window.runnerSudokuInput.keyboard++},true);
  });
  const page=await context.newPage();
  current=page;
  page.setDefaultTimeout(12000);
  page.on('pageerror',error=>{if(report.errors.length<30)report.errors.push({engine:browser.browserType().name(),message:bounded(error.message)})});
  page.on('response',response=>{
    const url=new URL(response.url());
    if(url.origin===options.origin&&response.status()>=400&&['document','script','stylesheet'].includes(response.request().resourceType())&&report.document_failures.length<12)
      report.document_failures.push({engine:browser.browserType().name(),path:url.pathname,type:response.request().resourceType(),status:response.status()});
    if(url.origin===options.origin&&response.status()>=400&&url.pathname.startsWith('/api/')&&report.errors.length<30)
      report.errors.push({engine:browser.browserType().name(),path:url.pathname,status:response.status()});
  });
  page.on('requestfailed',request=>{
    const url=new URL(request.url());
    if(url.origin===options.origin&&['document','script','stylesheet'].includes(request.resourceType())&&report.document_failures.length<12)
      report.document_failures.push({engine:browser.browserType().name(),path:url.pathname,type:request.resourceType(),message:boundedFailure(request.failure()?.errorText,500)});
  });
  return {context,page};
}

async function action(page,locator,touch,{secondary=false,boardSelector}={}) {
  assert.equal(await locator.count(),1,'one actual UI target is required');
  if(secondary)await locator.scrollIntoViewIfNeeded();
  const point=await locator.evaluate(node=>{
    const r=node.getBoundingClientRect(),x=r.x+r.width/2,y=r.y+r.height/2;
    return {x,y,hit:r.width>0&&r.height>0&&x>=0&&x<innerWidth&&y>=0&&y<innerHeight&&node.contains(document.elementFromPoint(x,y))};
  });
  assert.equal(point.hit,true,'UI action centre is offscreen or covered');
  if(touch)await locator.tap();else await locator.click();
  await settle(page,boardSelector);
}

const cell=(page,index)=>page.locator(`.sudoku-play .puzzle-board [data-cell="${index}"]`);
const digit=(page,value)=>page.locator('.sudoku-play .number-pad button').filter({has:page.locator('span').filter({hasText:new RegExp(`^${value}$`)})});
const tool=(page,name)=>page.locator('.sudoku-play .input-tools button').filter({hasText:new RegExp(`^\\s*${name}(?:\\s|$)`)});
const optionsSummary=page=>page.locator('.sudoku-play .play-settings > summary');

async function snapshots(page) {
  return page.evaluate(()=>Object.keys(localStorage).sort().flatMap(key=>{
    if(!key.startsWith('games-site:'))return [];
    try{const value=JSON.parse(localStorage.getItem(key));return value?.snapshot?[{key,snapshot:value.snapshot}]:[]}catch{return []}
  }));
}

async function state(page) {
  const m=await measure(page);
  const rendered=await page.locator('.sudoku-play .puzzle-board [data-cell]').evaluateAll(nodes=>nodes.map(node=>({
    index:Number(node.dataset.cell),value:node.querySelector(':scope > span:not(.cell-notes)')?.textContent.trim()??'',
    notes:[...node.querySelectorAll('.cell-notes > span')].map(n=>n.textContent.trim()),given:node.classList.contains('given'),
  })));
  return {snapshots:await snapshots(page),rendered,selected:m.selected,pinned:m.pinned,notesMode:m.notesMode,focus:m.focus};
}

function samePlay(actual,expected,{focus=true,pin=true}={}) {
  for(const key of ['snapshots','rendered','selected','notesMode',...(pin?['pinned']:[]),...(focus?['focus']:[])])assert.deepEqual(actual[key],expected[key],`presentation transition changed ${key}`);
}

async function load(page,boardPath) {
  const proof=await reopenSudoku(page,options.origin,boardPath);
  await settle(page);
  return proof;
}

async function createBoard(page,touch) {
  const admissions=[];
  const observe=response=>{
    if(new URL(response.url()).origin!==options.origin||!['/api/games/admit','/api/games/next'].includes(new URL(response.url()).pathname)||response.request().method()!=='POST'||response.status()!==200)return;
    const pending=(async()=>{
      const body=await response.body();assert.ok(body.length<=1<<20,'normal admission envelope exceeds bound');
      const envelope=JSON.parse(body),request=response.request().postDataJSON();
      assert.equal(envelope.status,'success','normal UI admission did not succeed');
      assert.deepEqual(envelope.request,request.request??request,'normal admission request binding differs');
      return envelope.publication??envelope.result?.publication;
    })();pending.catch(()=>{});admissions.push(pending);
  };
  page.on('response',observe);
  try {
    await page.goto(options.origin+'/',{waitUntil:'domcontentloaded'});
    const choose=page.getByRole('link',{name:'Play Sudoku',exact:true});
    await choose.scrollIntoViewIfNeeded();
    if(touch)await choose.tap();else await choose.click();
    const dialog=page.getByRole('dialog',{name:'New Sudoku',exact:true});
    await dialog.waitFor({state:'visible'});
    const easy=dialog.getByRole('button',{name:'Easy',exact:true});
    if(await easy.count())await easy.click();
    const play=dialog.getByRole('button',{name:'Play Sudoku',exact:true});
    await page.waitForFunction(()=>[...document.querySelectorAll('dialog[open] button')].some(n=>n.textContent.trim()==='Play Sudoku'&&!n.disabled));
    if(touch)await play.tap();else await play.click();
    await page.waitForURL(url=>/^\/sudoku\/[A-Za-z0-9_-]+$/.test(url.pathname));
    await page.locator('.sudoku-play .puzzle-board [data-cell="80"]').waitFor({state:'visible'});
    await settle(page);
  } finally {page.off('response',observe)}
  const transfers=await Promise.all(admissions),boardPath=new URL(page.url()).pathname,code=boardPath.split('/').at(-1);
  const transfer=transfers.find(t=>{const p=t?.puzzle??t?.identity;return p?.game==='sudoku'&&p.id?.split(':').at(-1)===code});
  const definition=transfer?.puzzle??transfer?.definition,answer=transfer?.answer;
  assert.ok(definition&&definition.rows===9&&definition.columns===9&&Array.isArray(answer)&&answer.length===81&&answer.every(n=>Number.isInteger(n)&&n>=1&&n<=9),'actual admitted 9×9 Sudoku answer/identity is unavailable');
  const visible=await state(page);
  assert.equal(visible.rendered.length,81,'actual UI board differs from admitted geometry');
  for(const row of visible.rendered.filter(row=>row.given))assert.equal(Number(row.value),answer[row.index],'visible given differs from actual admitted answer');
  return {boardPath,answer,identity:definition.id,publication:hash(transfer)};
}

async function geometryCase(page,engine,viewport,phase,scheme) {
  const row={engine,viewport:viewport.id,width:viewport.width,height:viewport.height,touch:viewport.touch,phase,scheme,status:'failed'};
  report.geometry.push(row);
  try {await settle(page);row.metrics=await measure(page);row.proof=assertGeometry(row.metrics,viewport);row.status='passed'}
  catch(error){row.failure=bounded(error.message)}
  await save();
  return row;
}

async function freshGeometry(browser,engine,recipe) {
  for(const viewport of viewports) {
    const {context,page}=await contextFor(browser,viewport);
    try {
      await load(page,recipe.boardPath);
      await geometryCase(page,engine,viewport,'fresh','light');
      await capture(page,engine,viewport.id+'-initial-light');
      const before=await state(page);
      await page.emulateMedia({colorScheme:'dark'});await settle(page);
      samePlay(await state(page),before);
      await geometryCase(page,engine,viewport,'fresh','dark');
      if(['phone-390','duo-inner-landscape','tablet-portrait','desktop'].includes(viewport.id))await capture(page,engine,viewport.id+'-initial-dark');
    } catch(error) {
      report.journeys.push({engine,id:'fresh-'+viewport.id,status:'failed',failure:bounded(error.message)});
    } finally {await context.close()}
  }
}

async function setOptions(page,touch,operation) {
  const details=page.locator('.sudoku-play .play-settings');
  if(await details.getAttribute('open')===null)await action(page,optionsSummary(page),touch,{secondary:true});
  await operation(details);
  await action(page,optionsSummary(page),touch,{secondary:true});
  await page.locator('.sudoku-play .player-board-frame').scrollIntoViewIfNeeded();await settle(page);
}

async function pinOption(page,touch,value) {
  await setOptions(page,touch,async()=>{
    const select=page.locator('.sudoku-play .pin-option select');
    await select.selectOption(String(value));
  });
  assert.deepEqual((await measure(page)).pinned,value?[value]:[],'Options repeated-entry selection is not reflected on the pad');
}

async function notesOn(page,touch,wanted=true) {
  const button=tool(page,'Notes');
  if((await button.getAttribute('aria-pressed')==='true')!==wanted)await action(page,button,touch);
}

async function firstEmpty(page) {
  const rows=(await state(page)).rendered;
  const empty=rows.find(row=>!row.given&&!row.value);
  assert.ok(empty,'normal UI has no editable empty cell');
  return empty.index;
}

async function resizeJourney(browser,engine,recipe,touch) {
  const cases=viewports.filter(v=>v.touch===touch),initial=cases.find(v=>v.id===(touch?'phone-390':'desktop'));
  const {context,page}=await contextFor(browser,initial);
  try {
    await load(page,recipe.boardPath);
    const index=await firstEmpty(page);
    await action(page,cell(page,index),touch);
    await notesOn(page,touch);
    for(let value=1;value<=9;value++)await action(page,digit(page,value),touch);
    const denseNotes=(await state(page)).rendered[index].notes.filter(Boolean).length;
    assert.ok(denseNotes>=6,'dense-note setup lacks six actual UI note entries');
    await pinOption(page,touch,recipe.answer[index]);
    await cell(page,index).focus();
    const before=await state(page);
    for(const side of touch?['right','left']:['latent']) {
      if(touch) {
        await setOptions(page,true,async()=>{await action(page,page.locator('.sudoku-play .controls-side').getByRole('button',{name:side==='left'?'Left':'Right',exact:true}),true,{secondary:true})});
        await cell(page,index).focus();
      }
      for(const viewport of cases) {
      await page.setViewportSize({width:viewport.width,height:viewport.height});await settle(page);
      samePlay(await state(page),before);
      const row=await geometryCase(page,engine,viewport,'in-progress-'+side,'light');
      if(touch&&viewport.width>viewport.height&&row.status==='passed')assert.equal(row.proof.side,side,'panel side preference changed during resize');
      // Use actual post-resize hit coordinates, then restore through Undo.
      await action(page,cell(page,index),touch,{secondary:!touch});
      const changed=await state(page);
      assert.notDeepEqual(changed.snapshots,before.snapshots,'real post-resize activation did not toggle the pinned note');
      assert.deepEqual(changed.rendered.filter(row=>row.index!==index),before.rendered.filter(row=>row.index!==index),'post-resize touch edited a neighbouring cell');
      assert.notEqual(changed.rendered[index].notes[recipe.answer[index]-1],before.rendered[index].notes[recipe.answer[index]-1],'post-resize touch did not toggle the intended pinned note');
      await action(page,tool(page,'Undo'),touch,{secondary:!touch});
      samePlay(await state(page),before,{focus:false});
      await cell(page,index).focus();
      }
    }
    await page.setViewportSize({width:initial.width,height:initial.height});await settle(page);
    for(const scheme of ['light','dark']){await page.emulateMedia({colorScheme:scheme});await settle(page);await capture(page,engine,(touch?'phone':'desktop')+'-dense-notes-'+scheme)}
    return {transitions:cases.length*(touch?2:1),retained:hash(before),tap_and_undo_each:true,notes:denseNotes,pinned:recipe.answer[index],focus_retained:true,sides:touch?['right','left']:['latent']};
  } finally {await context.close()}
}

async function hold(page,engine,value,{cancel=false,move=false}={}) {
  const key=digit(page,value),rect=await key.boundingBox();assert.ok(rect,'hold digit is absent');
  const x=rect.x+rect.width/2,y=rect.y+rect.height/2;
  if(engine==='chromium') {
    const cdp=await page.context().newCDPSession(page);let active=false;
    try {
      await cdp.send('Input.dispatchTouchEvent',{type:'touchStart',touchPoints:[{id:1,x,y}]});active=true;
      await pauseForHold();
      if(move)await cdp.send('Input.dispatchTouchEvent',{type:'touchMove',touchPoints:[{id:1,x:x+60,y:y+40}]});
      await cdp.send('Input.dispatchTouchEvent',{type:cancel?'touchCancel':'touchEnd',touchPoints:[]});active=false;
    } finally {if(active)await cdp.send('Input.dispatchTouchEvent',{type:'touchCancel',touchPoints:[]});await cdp.detach()}
  } else {
    await page.mouse.move(x,y);await page.mouse.down();
    try {await pauseForHold();if(move||cancel)await page.mouse.move(x+60,y+40)}finally{await page.mouse.up()}
  }
  await settle(page);
}

async function entryJourney(browser,engine,recipe) {
  const viewport=viewports.find(v=>v.id==='phone-390'),{context,page}=await contextFor(browser,viewport);
  try {
    await load(page,recipe.boardPath);
    const index=await firstEmpty(page),value=recipe.answer[index];
    await action(page,cell(page,index),true);
    assert.deepEqual((await measure(page)).pinned,[],'fresh board must start cell-first');
    const before=await state(page);
    await action(page,digit(page,value),true);
    assert.equal(Number((await state(page)).rendered[index].value),value,'cell-first tap did not enter the selected digit');
    await action(page,tool(page,'Undo'),true);
    assert.deepEqual((await state(page)).snapshots,before.snapshots,'Undo did not restore cell-first entry');
    await hold(page,engine,value);
    assert.deepEqual((await measure(page)).pinned,[value],'valid hold/release did not pin');
    assert.deepEqual((await state(page)).snapshots,before.snapshots,'pinning wrote an answer');
    for(const scheme of ['light','dark']){await page.emulateMedia({colorScheme:scheme});await settle(page);await capture(page,engine,'phone-pinned-'+scheme)}
    await action(page,digit(page,value),true);
    assert.deepEqual((await measure(page)).pinned,[],'tapping the pinned digit did not exit');
    await hold(page,engine,value,{cancel:true});
    assert.deepEqual((await measure(page)).pinned,[],'cancelled hold pinned a digit');
    assert.deepEqual((await state(page)).snapshots,before.snapshots,'cancelled hold or late release wrote progress');
    await hold(page,engine,value,{move:true});
    assert.deepEqual((await measure(page)).pinned,[],'moved/cancelled hold pinned a digit');
    assert.deepEqual((await state(page)).snapshots,before.snapshots,'moved hold or late release wrote progress');
    await pinOption(page,true,value);
    await cell(page,index).focus();await page.keyboard.press('Escape');await settle(page);
    assert.deepEqual((await measure(page)).pinned,[],'keyboard Escape did not exit Options pin');
    await notesOn(page,true);
    await action(page,digit(page,value),true);
    assert.equal((await state(page)).rendered[index].notes[value-1],String(value),'Notes tap did not toggle actual pencil mark');
    await action(page,tool(page,'Erase'),true);
    assert.equal((await state(page)).rendered[index].notes.filter(Boolean).length,0,'Erase did not clear selected notes');
    await action(page,tool(page,'Undo'),true);
    assert.equal((await state(page)).rendered[index].notes[value-1],String(value),'Undo did not restore erased note');
    await setOptions(page,true,async()=>{});
    const more=page.locator('.sudoku-play .more-puzzle-actions > summary');
    await action(page,more,true,{secondary:true});
    const saved=await snapshots(page),visible=(await state(page)).rendered;
    await page.reload({waitUntil:'domcontentloaded'});await settle(page);
    assert.deepEqual(await snapshots(page),saved,'in-progress reload changed saved progress');
    assert.deepEqual((await state(page)).rendered,visible,'in-progress reload changed visible entries/notes');
    await page.goto(options.origin+'/',{waitUntil:'domcontentloaded'});
    const resume=page.getByRole('button',{name:/^Resume Sudoku/});await resume.waitFor({state:'visible'});await resume.tap();await settle(page);
    assert.deepEqual((await state(page)).rendered,visible,'home Resume did not restore in-progress entries/notes');
    await action(page,page.locator('.sudoku-play .more-puzzle-actions').getByRole('button',{name:'Redo',exact:true}),true,{secondary:true});
    assert.equal((await state(page)).rendered[index].notes.filter(Boolean).length,0,'secondary Redo did not restore erase');
    await action(page,more,true,{secondary:true});
    const input=await page.evaluate(()=>window.runnerSudokuInput);report.input.touch+=input.touch;report.input.mouse+=input.mouse;report.input.keyboard+=input.keyboard;
    assert.ok(input.touch>0,'entry journey did not produce trusted touches');
    return {cell_first:true,hold:engine==='chromium'?'trusted-touch':'trusted-mouse-WebKit-limitation',cancel_and_move:true,options_pin:true,escape:true,notes_erase_undo_redo:true,reload:true,home_resume:true,input};
  } finally {await context.close()}
}

async function sideJourney(browser,engine,recipe) {
  const viewport=viewports.find(v=>v.id==='duo-inner-landscape'),{context,page}=await contextFor(browser,viewport);
  try {
    await load(page,recipe.boardPath);
    const index=await firstEmpty(page),value=recipe.answer[index];
    const initial=await measure(page);assert.ok(initial.controls.x>initial.frame.x,'touch side must initially be right');
    await action(page,cell(page,index),true);await notesOn(page,true);await action(page,digit(page,value),true);await pinOption(page,true,value);
    const before=await state(page);
    await setOptions(page,true,async()=>{await action(page,page.locator('.sudoku-play .controls-side').getByRole('button',{name:'Left',exact:true}),true,{secondary:true})});
    const left=await measure(page);assert.ok(left.controls.x<left.frame.x,'Left did not swap the complete panels');
    samePlay(await state(page),before,{focus:false});
    assertGeometry(left,viewport);
    await capture(page,engine,'duo-left-pinned-light');
    for(const v of [viewports.find(v=>v.id==='tablet-portrait'),viewports.find(v=>v.id==='duo-inner-landscape')]) {
      await page.setViewportSize({width:v.width,height:v.height});await settle(page);samePlay(await state(page),before,{focus:false});assertGeometry(await measure(page),v);
    }
    assert.ok((await measure(page)).controls.x<(await measure(page)).frame.x,'side preference was lost on portrait/landscape return');
    const saved=await snapshots(page);await page.reload({waitUntil:'domcontentloaded'});await settle(page);
    assert.deepEqual(await snapshots(page),saved,'side preference reload changed saved progress');
    assert.ok((await measure(page)).controls.x<(await measure(page)).frame.x,'side preference did not survive reload');
    const oldPath=new URL(page.url()).pathname;
    await action(page,page.getByRole('button',{name:'New puzzle',exact:true}),true,{secondary:true});
    await page.waitForURL(url=>/^\/sudoku\//.test(url.pathname)&&url.pathname!==oldPath);await settle(page);
    assert.ok((await measure(page)).controls.x<(await measure(page)).frame.x,'side preference did not survive a new puzzle');
    const freshSaved=await snapshots(page);for(const old of saved)assert.deepEqual(freshSaved.find(row=>row.key===old.key),old,'New puzzle lost the prior saved board');
    return {right_initially:true,left_swap:true,progress_pin_notes_retained:true,portrait_return:true,persisted_reload:true,persisted_new_puzzle:true};
  } finally {await context.close()}
}

async function hintJourney(browser,engine,recipe) {
  const viewport=viewports.find(v=>v.id==='phone-390'),{context,page}=await contextFor(browser,viewport);
  try {
    await load(page,recipe.boardPath);
    const index=await firstEmpty(page),value=recipe.answer[index];
    await action(page,cell(page,index),true);await notesOn(page,true);await action(page,digit(page,value),true);await pinOption(page,true,value);
    const before=await snapshots(page),pin=(await measure(page)).pinned;
    await action(page,tool(page,'Hint'),true);
    const panel=page.locator('.sudoku-play .hint-panel:visible');await panel.waitFor({state:'visible'});
    await page.waitForFunction(()=>document.querySelector('.sudoku-play .hint-panel')?.getAttribute('aria-busy')!=='true');
    const request=panel.getByRole('button',{name:'A little hint',exact:true});
    if(await request.isVisible())await action(page,request,true,{secondary:true});
    const reveal=panel.getByRole('button',{name:'Show the next move',exact:true});await reveal.waitFor({state:'visible'});
    await action(page,panel.getByRole('button',{name:'Show why',exact:true}),true,{secondary:true});
    await panel.locator('.hint-why').waitFor({state:'visible'});
    assert.ok((await panel.locator('.hint-why').innerText()).trim().length>20,'requested explanation is empty');
    for(const scheme of ['light','dark']){await page.emulateMedia({colorScheme:scheme});await settle(page);assertGeometry(await measure(page),viewport,{hint:true});await capture(page,engine,'phone-hint-why-'+scheme)}
    await action(page,reveal,true,{secondary:true});
    const final=panel.locator('.hint-final-step');
    if(await final.isVisible())await action(page,final,true,{secondary:true});
    await panel.locator('.hint-apply').waitFor({state:'visible'});
    assert.deepEqual(await snapshots(page),before,'reading a hint changed saved progress');
    assert.deepEqual((await measure(page)).pinned,pin,'reading a hint changed pinned entry mode');
    await action(page,panel.locator('.hint-evidence > summary'),true,{secondary:true});
    for(const scheme of ['light','dark']) {
      await page.emulateMedia({colorScheme:scheme});await settle(page);
      const metrics=await measure(page);assertGeometry(metrics,viewport,{hint:true});
      await capture(page,engine,'phone-hint-'+scheme);
    }
    await action(page,panel.locator('.hint-apply'),true,{secondary:true});
    assert.notDeepEqual(await snapshots(page),before,'Apply did not make the actual hinted move');
    await action(page,tool(page,'Undo'),true);
    assert.deepEqual(await snapshots(page),before,'Undo did not restore hint entries/notes');
    return {requested:true,explanation:true,board_visible:true,read_only_until_apply:true,apply_undo:true};
  } finally {await context.close()}
}

async function completionJourney(browser,engine,recipe,touch=false) {
  const viewport=viewports.find(v=>v.id===(touch?'phone-390':'desktop')),{context,page}=await contextFor(browser,viewport);
  try {
    await load(page,recipe.boardPath);
    const rows=(await state(page)).rendered;
    for(const row of rows.filter(row=>!row.given)) {
      await action(page,cell(page,row.index),touch,{secondary:!touch});
      await action(page,digit(page,recipe.answer[row.index]),touch,{secondary:!touch});
    }
    const dialog=page.getByRole('dialog',{name:'Puzzle solved!',exact:true});await dialog.waitFor({state:'visible'});
    for(const scheme of ['light','dark']){await page.emulateMedia({colorScheme:scheme});await settle(page);await capture(page,engine,(touch?'phone':'desktop')+'-result-'+scheme)}
    await action(page,dialog.getByRole('button',{name:'View board',exact:true}),touch,{secondary:true});
    const saved=await snapshots(page),rendered=(await state(page)).rendered;
    await page.reload({waitUntil:'domcontentloaded'});await settle(page);
    assert.deepEqual(await snapshots(page),saved,'solved progress did not survive reload');
    assert.deepEqual((await state(page)).rendered,rendered,'solved visible values did not survive reload');
    await page.locator('.sudoku-play .completion').waitFor({state:'visible'});
    await page.goto(options.origin+'/',{waitUntil:'domcontentloaded'});
    const view=page.getByRole('button',{name:/^View board Sudoku/});await view.waitFor({state:'visible'});await view.click();await settle(page);
    assert.deepEqual((await state(page)).rendered,rendered,'home result view did not restore solved board');
    const oldPath=new URL(page.url()).pathname;
    const fresh=page.getByRole('button',{name:'New puzzle',exact:true});await action(page,fresh,touch,{secondary:true});
    await page.waitForURL(url=>/^\/sudoku\//.test(url.pathname)&&url.pathname!==oldPath);await settle(page);
    assert.equal(await page.locator('.sudoku-play .completion:visible').count(),0,'New puzzle retained completed state');
    return {publication:recipe.publication,normal_UI_entries:rows.filter(row=>!row.given).length,solved:true,result_light_dark:true,reload:true,home_view:true,new_puzzle:true};
  } finally {await context.close()}
}

async function keyboardJourney(browser,engine,recipe) {
  const viewport=viewports.find(v=>v.id==='desktop-short'),{context,page}=await contextFor(browser,viewport);
  try {
    await load(page,recipe.boardPath);
    const index=await firstEmpty(page),value=recipe.answer[index];
    await action(page,cell(page,index),false,{secondary:true});
    await page.keyboard.press(String(value));await settle(page);
    assert.equal(Number((await state(page)).rendered[index].value),value,'desktop focused keyboard digit did not enter');
    await page.keyboard.press('ControlOrMeta+z');await settle(page);
    assert.equal((await state(page)).rendered[index].value,'','desktop keyboard Undo did not restore entry');
    const selected=(await measure(page)).selected;await page.keyboard.press('ArrowRight');await settle(page);
    assert.notEqual((await measure(page)).selected,selected,'desktop arrow key did not move selection');
    const next=Number((await measure(page)).selected);assert.equal((await measure(page)).focus,String(next),'desktop arrow key lost cell focus');
    await cell(page,index).focus();await page.keyboard.press('n');await page.keyboard.press(String(value));await settle(page);
    assert.equal((await state(page)).rendered[index].notes[value-1],String(value),'desktop keyboard notes did not toggle');
    await page.keyboard.press('Delete');await settle(page);
    assert.equal((await state(page)).rendered[index].notes.filter(Boolean).length,0,'desktop Delete did not erase notes');
    const given=(await state(page)).rendered.find(row=>row.given);await cell(page,given.index).focus();await page.keyboard.press(String(given.value)==='1'?'2':'1');await settle(page);
    assert.equal((await state(page)).rendered[given.index].value,given.value,'keyboard changed a protected given');
    await page.keyboard.press('Tab');assert.equal(await page.locator('.sudoku-play .puzzle-board').evaluate(n=>n.contains(document.activeElement)),false,'Tab did not leave the board');
    const input=await page.evaluate(()=>window.runnerSudokuInput);report.input.keyboard+=input.keyboard;
    assert.ok(input.keyboard>=8,'desktop keyboard proof lacks trusted key events');
    return {short_window:[viewport.width,viewport.height],digit:true,undo:true,arrows_focus:true,notes_erase:true,givens_protected:true,tab_exit:true,input};
  } finally {await context.close()}
}

async function appearanceJourney(browser,engine,recipe) {
  const viewport=viewports.find(v=>v.id==='phone-390'),{context,page}=await contextFor(browser,viewport);
  try {
    await load(page,recipe.boardPath);
    const before=await snapshots(page),seen={};
    for(const choice of ['dark','light','system']) {
      await action(page,page.locator('.appearance-trigger:visible').first(),true,{secondary:true});
      await action(page,page.getByRole('menuitemradio',{name:choice[0].toUpperCase()+choice.slice(1),exact:true}),true);
      await page.reload({waitUntil:'domcontentloaded'});await settle(page);
      const actual=await page.evaluate(()=>({choice:document.documentElement.dataset.appearance,saved:localStorage.getItem('games-site:appearance')}));
      assert.deepEqual(actual,{choice,saved:choice},'actual appearance control did not persist across reload');
      assert.deepEqual(await snapshots(page),before,'appearance choice changed saved Sudoku progress');
      seen[choice]=await palette(page);
    }
    assert.ok(seen.dark.page&&seen.light.page&&seen.dark.page!==seen.light.page,'explicit Light/Dark controls did not change the actual palette');
    const system=[];
    for(const scheme of ['dark','light']){await page.emulateMedia({colorScheme:scheme});await settle(page);system.push(await palette(page))}
    assert.ok(system.every(p=>p.choice==='system'&&p.page)&&system[0].page!==system[1].page,'System appearance did not follow the actual emulated preference');
    return {explicit_controls:true,persisted_reload:true,system_follows_media:true,progress_unchanged:true,palettes:seen};
  } finally {await context.close()}
}

async function neighborMetrics(page,boardSelector) {
  return page.locator(boardSelector).evaluate(board=>{
    const rect=node=>{const r=node.getBoundingClientRect();return {x:r.x,y:r.y,width:r.width,height:r.height}};
    const frame=board.closest('.player-board-frame'),region=board.closest('.player-board-region');
    const controls=[...region.querySelectorAll('.number-pad button,.input-tools button,.value-controls button,.rectangle-controls button,.player-actions button')]
      .filter(node=>node.getClientRects().length&&getComputedStyle(node).visibility==='visible')
      .map(node=>({...rect(node),label:node.textContent.trim().slice(0,60)}));
    return {viewport:{width:innerWidth,height:innerHeight},documentWidth:document.documentElement.scrollWidth,board:rect(board),frame:rect(frame),
      internalWidth:board.scrollWidth,internalHeight:board.scrollHeight,clientWidth:board.clientWidth,clientHeight:board.clientHeight,controls};
  });
}

async function neighborPaint(page,boardSelector) {
  return page.locator(boardSelector+' [data-cell]').evaluateAll(nodes=>nodes.map(node=>({
    cell:node.getAttribute('data-cell'),value:node.getAttribute('data-value'),fill:node.style.getPropertyValue('--rectangle-fill'),
    text:[...node.childNodes].filter(n=>n.nodeType===Node.TEXT_NODE).map(n=>n.textContent.trim()).join('')+
      [...node.children].filter(n=>n.tagName==='SPAN'&&!n.matches('.cell-notes,.wrong-mark,.conflict-mark,.corner-mark')).map(n=>n.textContent.trim()).join(''),
    notes:[...node.querySelectorAll('.cell-notes > span')].map(n=>n.textContent.trim()),
  })));
}

async function neighborSmoke(browser,game) {
  const viewport=viewports.find(v=>v.id==='desktop'),{context,page}=await contextFor(browser,viewport);
  const title={suguru:'Suguru',nonogram:'Nonogram',shikaku:'Shikaku'}[game];
  const {root,board:selector,domBoard}=neighborSelectors(game);
  try {
    if(game==='nonogram') {
      // Normal public reopening is independent of New's executable-bound
      // engine qualification. This tiny shared-shell smoke never waives New.
      await page.goto(options.origin+'/nonogram/ngm1-5-easy-101',{waitUntil:'domcontentloaded'});
    } else {
      await page.goto(options.origin+'/',{waitUntil:'domcontentloaded'});
      await page.getByRole('link',{name:'Choose '+title,exact:true}).click();
      const dialog=page.getByRole('dialog',{name:'New '+title,exact:true});await dialog.waitFor({state:'visible'});
      const sizes=dialog.getByRole('group',{name:'Board size',exact:true});
      if(await sizes.count())await sizes.locator('button:not([disabled])').first().click();
      const easy=dialog.getByRole('button',{name:'Easy',exact:true});if(await easy.count())await easy.click();
      await page.waitForFunction(label=>[...document.querySelectorAll('dialog[open] button')].some(n=>n.textContent.trim()===label&&!n.disabled),'Play '+title);
      await dialog.getByRole('button',{name:'Play '+title,exact:true}).click();
      await page.waitForURL(url=>new RegExp(`^/${game}/[A-Za-z0-9_-]+$`).test(url.pathname));
    }
    const board=page.locator(selector);await board.locator('[data-cell]').last().waitFor({state:'visible'});await settle(page,domBoard);
    const geometry=await neighborMetrics(page,selector),composition=assertNeighborGeometry(geometry,{underBoard:game==='suguru'});
    const before=await snapshots(page),paint=await neighborPaint(page,selector);
    const click=locator=>action(page,locator,false,{boardSelector:domBoard});
    if(game==='shikaku') {
      const count=await board.locator('[data-cell]').count(),n=Math.sqrt(count);
      assert.ok(Number.isInteger(n)&&n>=5&&n<=7,'neighbor smoke must stay on a small Shikaku board');
      await click(board.locator('[data-cell="0"]'));await click(board.locator(`[data-cell="${n+1}"]`));
      await click(page.locator(root).getByRole('button',{name:'Add rectangle',exact:true}));
      assert.equal((await neighborPaint(page,selector)).filter(row=>row.fill.trim()).length,4,'small Shikaku rectangle did not paint exactly its intended four cells');
    } else {
      const target=game==='nonogram'?board.locator('[data-cell="0"]'):board.locator('[data-cell]:not(.given):not([data-given="true"])').filter({hasText:/^\s*$/}).first();
      await click(target);
      const control=game==='nonogram'?page.locator(root).getByRole('button',{name:/Shade/}):page.locator(root+' .number-pad button').filter({hasText:/^\s*1\s*$/}).first();
      await click(control);
    }
    const edited=await snapshots(page);assert.notDeepEqual(edited,before,'neighbor normal UI input did not change its saved snapshot');
    assert.notDeepEqual(await neighborPaint(page,selector),paint,'neighbor input did not change its visible board');
    await click(page.locator(root).getByRole('button',{name:'Undo',exact:true}));
    assert.deepEqual(await snapshots(page),before,'shared Undo did not restore neighbor progress');
    assert.deepEqual(await neighborPaint(page,selector),paint,'shared Undo did not restore the visible neighbor board');
    if(game==='suguru') {
      const settings=page.locator(root+' .play-settings > summary');
      assert.equal((await settings.innerText()).trim(),'Settings','shared PlaySettings changed the Suguru default label');
      await click(settings);
      const timer=page.locator(root+' .play-settings').getByRole('checkbox',{name:'Show timer',exact:true}),original=await timer.isChecked();
      await click(timer);assert.equal(await timer.isChecked(),!original,'shared Settings checkbox did not activate');
      await click(timer);assert.equal(await timer.isChecked(),original,'shared Settings checkbox did not restore');
      await click(settings);assert.deepEqual(await snapshots(page),before,'shared Settings changed Suguru progress');
    }
    return {game,scope:'affected shared-shell smoke only',entry:game==='nonogram'?'normal public catalogue-code reopen; no New-generation qualification':'small normal UI New',
      path:new URL(page.url()).pathname,geometry:composition,input_undo:true,settings_default:game==='suguru'?'checked':'not repeated',before:hash(before),edited:hash(edited),source_bound:true};
  } finally {await context.close()}
}

async function stop() {stopping=true;if(activeBrowser)try{await activeBrowser.close()}catch{}}
process.once('SIGTERM',stop);process.once('SIGINT',stop);
try {
  for(const engine of [{name:'chromium',type:chromium,executable:options.chromium},{name:'webkit',type:webkit,executable:options.webkit}]) {
    let recipe;
    try {
      activeBrowser=await engine.type.launch({executablePath:engine.executable,headless:true});
      report.engines.push({name:engine.name,version:activeBrowser.version(),executable:engine.executable,input:'headless emulation'});
      const {context,page}=await contextFor(activeBrowser,viewports.find(v=>v.id==='phone-390'));
      try{await check(engine.name,'normal-ui-admission',async()=>{recipe=await createBoard(page,true);return {path:recipe.boardPath,identity:recipe.identity,publication:recipe.publication}})}finally{await context.close()}
      const ready=recipe&&await afterReopen(async()=>{
        const {context,page}=await contextFor(activeBrowser,viewports.find(v=>v.id==='phone-390'));
        try{return await check(engine.name,'public-code-reopen',()=>load(page,recipe.boardPath),{rows:report.prerequisites,failureStatus:'blocked'})}
        finally{await context.close()}
      },async()=>{
        await freshGeometry(activeBrowser,engine.name,recipe);
        await check(engine.name,'entry-pin-cancel-history',()=>entryJourney(activeBrowser,engine.name,recipe));
        await check(engine.name,'touch-in-progress-resize',()=>resizeJourney(activeBrowser,engine.name,recipe,true));
        await check(engine.name,'desktop-in-progress-resize',()=>resizeJourney(activeBrowser,engine.name,recipe,false));
        await check(engine.name,'side-swap-persistence',()=>sideJourney(activeBrowser,engine.name,recipe));
        await check(engine.name,'hint-explanation-apply-undo',()=>hintJourney(activeBrowser,engine.name,recipe));
        await check(engine.name,'completion-new-resume',()=>completionJourney(activeBrowser,engine.name,recipe));
        await check(engine.name,'touch-completion-new-resume',()=>completionJourney(activeBrowser,engine.name,recipe,true));
        await check(engine.name,'desktop-keyboard-focus',()=>keyboardJourney(activeBrowser,engine.name,recipe));
        await check(engine.name,'appearance-controls',()=>appearanceJourney(activeBrowser,engine.name,recipe));
      });
      if(!ready)report.unrun.push({engine:engine.name,blocked_by:recipe?'public-code-reopen':'normal-ui-admission',scope:'Sudoku fresh/in-progress geometry and functional journeys',journeys:functionalJourneys});
      if(engine.name==='chromium')for(const game of ['suguru','nonogram','shikaku'])
        await check(engine.name,'shared-'+game+'-smoke',()=>neighborSmoke(activeBrowser,game),{rows:report.neighbors});
    } catch(error){report.journeys.push({engine:engine.name,id:'runtime',status:'failed',failure:bounded(error.message)})}
    finally {if(activeBrowser){await activeBrowser.close();activeBrowser=undefined}}
  }
  const expectedGeometry=2*(viewports.length*2+viewports.filter(v=>v.touch).length*2+viewports.filter(v=>!v.touch).length);
  const missingJourneys=['chromium','webkit'].flatMap(engine=>['normal-ui-admission',...functionalJourneys].filter(id=>!report.journeys.some(row=>row.engine===engine&&row.id===id)).map(id=>({engine,id})));
  report.coverage={required_engines:2,observed_engines:report.engines.length,required_geometry:expectedGeometry,observed_geometry:report.geometry.length,
    required_journeys:20,missing_journeys:missingJourneys,required_neighbors:3,observed_neighbors:report.neighbors.length};
  report.coverage.complete=report.engines.length===2&&report.geometry.length===expectedGeometry&&missingJourneys.length===0&&report.neighbors.length===3;
  report.status=report.unrun.length||!report.coverage.complete?'blocked':failures()===0?'passed':'failed';
} catch(error){report.failure=bounded(error.message)}
finally {
  if(activeBrowser)try{await activeBrowser.close();activeBrowser=undefined}catch(error){report.errors.push({cleanup:bounded(error.message)})}
  report.browser_closed=!activeBrowser;
  if(report.engines.length!==2||report.journeys.some(row=>row.id==='runtime'))report.status='blocked';
  report.complete=true;
  await save();
}
