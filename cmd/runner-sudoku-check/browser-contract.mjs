import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';

export const sudokuBoardSelector='.sudoku-play .puzzle-board';

// Playwright visibility selectors never cross into native DOM selector APIs.
export function neighborSelectors(game) {
  assert.ok(['suguru','nonogram','shikaku'].includes(game),'unknown shared-shell smoke');
  const root=game==='suguru'?'.suguru-play':`.${game}-workspace`;
  const board=game==='suguru'?'.puzzle-board':`.${game}-board`;
  return {root:root+':visible',board:`${root}:visible ${board}`,domBoard:`${root} ${board}`};
}

export function boundedFailure(error,limit=1400) {
  return String(error?.message??error??'').replace(/\u001b\[[0-9;]*m/g,'').slice(0,limit);
}

export function recordFailure(row,error) {
  row.failure=boundedFailure(error);
  for(const kind of ['interaction','feedback_layout'])if(error[kind])row[kind]=error[kind];
}

// Read-only facts describe the actual target and browser scroll state. Disabled
// controls still have layout footprints; they are not executable actions.
export function targetFacts(node) {
  const r=node.getBoundingClientRect(),x=r.x+r.width/2,y=r.y+r.height/2,cover=document.elementFromPoint(x,y);
  const describe=n=>n?{tag:n.tagName,label:(n.getAttribute('aria-label')??n.textContent??'').trim().slice(0,80),cell:n.getAttribute('data-cell')}:null;
  return {
    ...describe(node),x:r.x,y:r.y,width:r.width,height:r.height,centre:{x,y},
    enabled:!node.matches(':disabled')&&node.getAttribute('aria-disabled')!=='true',
    in_viewport:r.width>0&&r.height>0&&x>=0&&x<innerWidth&&y>=0&&y<innerHeight,
    hit:!!cover&&node.contains(cover),cover:describe(cover),
  };
}

export function interactionFacts(boardSelector) {
  const visible=n=>n.getClientRects().length&&getComputedStyle(n).visibility==='visible';
  const board=[...document.querySelectorAll(boardSelector)].find(visible),region=board?.closest('.player-board-region');
  const rect=n=>{if(!n)return null;const r=n.getBoundingClientRect();return {x:r.x,y:r.y,width:r.width,height:r.height}};
  const focus=document.activeElement;
  return {
    path:location.pathname,viewport:{width:innerWidth,height:innerHeight},scroll:{x:scrollX,y:scrollY},
    board:rect(board),frame:rect(board?.closest('.player-board-frame')),controls:rect(region?.querySelector('.play-controls-panel,.primary-inputs,.value-controls,.rectangle-controls')),
    selected:board?.querySelector('.selected,[aria-selected="true"]')?.getAttribute('data-cell')??null,
    pinned:[...(region?.querySelectorAll('.number-pad button[aria-pressed="true"]')??[])].map(n=>n.textContent.trim()).slice(0,9),
    focus:focus?{tag:focus.tagName,cell:focus.getAttribute('data-cell'),label:(focus.getAttribute('aria-label')??focus.textContent??'').trim().slice(0,80)}:null,
  };
}

export function neighborGeometry(board) {
  const rect=node=>{const r=node.getBoundingClientRect();return {x:r.x,y:r.y,width:r.width,height:r.height}};
  const frame=board.closest('.player-board-frame'),region=board.closest('.player-board-region');
  const controls=[...region.querySelectorAll('.number-pad button,.input-tools button,.value-controls button,.rectangle-controls button,.player-actions [data-action="undo"]')]
    .filter(node=>{const r=node.getBoundingClientRect();return node.getClientRects().length&&r.width>0&&r.height>0&&getComputedStyle(node).visibility==='visible'})
    .map(node=>({...rect(node),label:node.textContent.trim().slice(0,60)}));
  return {viewport:{width:innerWidth,height:innerHeight},documentWidth:document.documentElement.scrollWidth,board:rect(board),frame:rect(frame),
    internalWidth:board.scrollWidth,internalHeight:board.scrollHeight,clientWidth:board.clientWidth,clientHeight:board.clientHeight,controls};
}

export async function layoutReady(page) {
  await page.evaluate(async()=>{
    await document.fonts.ready;
    await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
  });
}

// All clicks/taps share one contract: resolve exactly one visible, enabled
// target, allow ordinary scrolling only when requested, then hit-test the fresh
// centre and deliver a trusted pointer event. No forced click or CSS override.
export async function activateTarget(page,locator,touch,{secondary=false,boardSelector=sudokuBoardSelector,after,observe=()=>{}}={}) {
  const evidence={locator:String(locator).slice(0,240),touch,secondary,phase:'locate'};
  try {
    evidence.before=await page.evaluate(interactionFacts,boardSelector);
    evidence.matches=await locator.count();
    assert.equal(evidence.matches,1,'one actual UI target is required');
    await locator.waitFor({state:'visible'});
    if(secondary) {
      evidence.phase='scroll';
      await locator.evaluate(node=>node.scrollIntoView({block:'nearest',inline:'nearest',behavior:'instant'}));
      await layoutReady(page);
      evidence.scrolled=await page.evaluate(interactionFacts,boardSelector);
    }
    if(!touch) {
      evidence.phase='pointer-prepare';
      evidence.pointer_before=await locator.evaluate(targetFacts);
      if(evidence.pointer_before.in_viewport) {
        // A mouse approaches its target before clicking. That ordinary move
        // can dismiss the previous control's hover UI or reveal new hover UI;
        // judge the actual resulting target rather than the parked pointer.
        const {x,y}=evidence.pointer_before.centre;
        await page.mouse.move(x,y);
        await layoutReady(page);
      }
    }
    evidence.phase='hit-test';
    evidence.target=await locator.evaluate(targetFacts);
    assert.equal(evidence.target.enabled,true,'UI action target is disabled');
    assert.equal(evidence.target.in_viewport,true,'UI action centre is offscreen');
    assert.equal(evidence.target.hit,true,'UI action centre is covered');
    evidence.phase='pointer';
    const {x,y}=evidence.target.centre;
    if(touch)await page.touchscreen.tap(x,y);else await page.mouse.click(x,y);
    evidence.phase='settle';
    if(after)await after();else await layoutReady(page);
    evidence.after=await page.evaluate(interactionFacts,boardSelector);
    evidence.phase='complete';
    observe(evidence);
  } catch(error) {
    try{evidence.after=await page.evaluate(interactionFacts,boardSelector)}catch{}
    observe(evidence);
    const failure=new Error(`${evidence.phase}: ${evidence.locator}\n${boundedFailure(error,900)}`,{cause:error});
    failure.interaction=evidence;
    throw failure;
  }
}

export function savedBoardAction(page,identity,{complete=false}={}) {
  // Reloaded saves may legitimately require public admission before completion
  // is known. Bind the action to the admitted identity, not a guessed label or
  // whichever Sudoku board happens to be first in the collection.
  assert.match(identity,/^[A-Za-z0-9:_-]{1,240}$/,'invalid admitted board identity');
  return page.locator(`.saved-row[data-board-id="${identity}"]`).getByRole('button',{name:complete?/^View board Sudoku\b/:/^(Resume|Open to check) Sudoku\b/});
}

export async function newSudokuFromPlay(page,activate) {
  const header=page.locator('.sudoku-play .play-topbar').getByRole('button',{name:'New puzzle',exact:true});
  if(await header.isVisible()) {
    await activate(header);
    return;
  }
  // New remains secondary after removal of More actions. Resolve its actual
  // visible control, or the existing Options disclosure; never a dialog's New.
  const play=page.locator('.sudoku-play');
  const target=play.getByRole('button',{name:'New puzzle',exact:true});
  if(await target.count()===0) {
    const settings=page.locator('.sudoku-play .play-settings');
    assert.equal(await settings.count(),1,'one Options disclosure is required for a concealed New action');
    if(await settings.getAttribute('open')===null)await activate(page.locator('.sudoku-play .play-settings > summary'));
  }
  assert.equal(await target.count(),1,'one visible in-play New puzzle action is required');
  await activate(target);
}

// These are candidate observations, not selectors supplied by the product.
export function feedbackLayoutFacts() {
  const play=document.querySelector('.sudoku-play'),board=play?.querySelector('.puzzle-board');
  const rect=node=>{if(!node)return null;const r=node.getBoundingClientRect();return {x:r.x,y:r.y,width:r.width,height:r.height,right:r.right,bottom:r.bottom}};
  const visible=node=>node.getClientRects().length&&getComputedStyle(node).visibility==='visible';
  const headers=[...(play?.querySelectorAll('.play-heading')??[])].filter(visible),header=headers[0];
  let headerContent;
  if(header) {
    const r=rect(header),style=getComputedStyle(header),px=property=>parseFloat(style[property])||0;
    headerContent={x:r.x+px('borderLeftWidth')+px('paddingLeft'),y:r.y+px('borderTopWidth')+px('paddingTop'),
      right:r.right-px('borderRightWidth')-px('paddingRight'),bottom:r.bottom-px('borderBottomWidth')-px('paddingBottom')};
  }
  const buttons=[...(play?.querySelectorAll('button')??[])].filter(visible);
  const actions=['Check','Restart'].map(label=>{
    const found=buttons.filter(node=>(node.getAttribute('aria-label')??node.textContent).trim()===label);
    const node=found[0],r=rect(node),x=r?r.x+r.width/2:0,y=r?r.y+r.height/2:0;
    return {label,count:found.length,rect:r,font:node?parseFloat(getComputedStyle(node).fontSize):0,
      inside_primary:!!node?.closest('.input-tools,.number-pad'),inside_header:!!node&&!!header?.contains(node),
      target:node?{enabled:!node.matches(':disabled')&&node.getAttribute('aria-disabled')!=='true',
        in_viewport:r.width>0&&r.height>0&&x>=0&&x<innerWidth&&y>=0&&y<innerHeight,
        hit:node.contains(document.elementFromPoint(x,y))}:null};
  });
  const primary=[...(play?.querySelectorAll('.input-tools button')??[])].filter(visible).map(node=>node.getBoundingClientRect().height);
  const text=play?.textContent??'';
  return {board:rect(board),frame:rect(board?.closest('.player-board-frame')),header:rect(header),header_count:headers.length,header_content:headerContent,
    actions,primary_height:primary.length?Math.min(...primary):0,
    repeated_entry:!!play?.querySelector('.pin-option')||/Repeated entry/i.test(play?.querySelector('.play-settings')?.textContent??''),
    more_actions:!!play?.querySelector('.more-puzzle-actions')||/More actions/i.test(text),
    unlock_highlight:buttons.some(node=>/Unlock highlight/i.test(node.textContent)),
    lock_prose:/Highlight\s+\d+\s+locked|Use Unlock highlight|Highlighting does not change the pinned/i.test(text)};
}

export function assertFeedbackLayout(facts) {
  let group;
  try {
    for(const name of ['repeated_entry','more_actions','unlock_highlight','lock_prose'])assert.equal(facts[name],false,`removed Sudoku UI remains: ${name}`);
    assert.ok(facts.board&&facts.frame,'actual board/frame are required for secondary-action geometry');
    assert.ok(facts.header&&facts.header_count===1&&facts.header_content,'one actual play header is required for secondary-action placement');
    assert.deepEqual(facts.actions.map(action=>action.label),['Check','Restart'],'Check and Restart measurements are required');
    for(const action of facts.actions) {
      assert.equal(action.count,1,`one visible ${action.label} action is required`);
      const r=action.rect;
      assert.ok(r&&r.width>0&&r.height>0&&facts.primary_height>0&&r.height<=facts.primary_height+1&&!action.inside_primary,`${action.label} is not a separate compact secondary action`);
      assert.equal(action.inside_header,true,`${action.label} is outside the play header`);
      assert.ok(r.bottom<=facts.board.y+1,`${action.label} is not above the board`);
      assert.equal(action.target?.in_viewport,true,`${action.label} target is offscreen`);
      if(action.target.enabled)assert.equal(action.target.hit,true,`${action.label} target is covered`);
    }
    const [check,restart]=facts.actions.map(action=>action.rect);
    group={x:Math.min(check.x,restart.x),y:Math.min(check.y,restart.y),right:Math.max(check.right,restart.right),bottom:Math.max(check.bottom,restart.bottom)};
    assert.ok(Math.max(check.y,restart.y)<Math.min(check.bottom,restart.bottom),'Check and Restart do not share a horizontal action row');
    assert.ok(check.right<=restart.x,'Check and Restart overlap or are out of order');
    const bounds=facts.header_content;
    assert.ok(group.x>=bounds.x&&group.right<=bounds.right&&group.y>=bounds.y&&group.bottom<=bounds.bottom,'Check and Restart group is outside the usable play header');
    assert.ok(bounds.right-group.right<group.x-bounds.x,'Check and Restart group is not on the right of the play header');
    return {removed_controls_absent:true,compact_right_board_actions:true,...facts,action_group:group};
  } catch(error) {
    error.feedback_layout={...facts,...(group?{action_group:group}:{})};
    throw error;
  }
}

export function lockedDigitFacts(node) {
  const style=getComputedStyle(node),span=node.querySelector('span');
  return {disabled:node.matches(':disabled')||node.getAttribute('aria-disabled')==='true',
    exhausted:node.classList.contains('exhausted'),highlighted:node.classList.contains('inspecting'),
    background:style.backgroundColor,color:style.color,opacity:span?Number(getComputedStyle(span).opacity):Number(style.opacity)};
}

// Geometry can settle before a button's color/opacity transitions finish.
// Observe the authored final paint without cancelling or advancing animations.
export async function paintedLockedDigitFacts(locator) {
  await locator.evaluate(async node=>{
    await Promise.allSettled(node.getAnimations({subtree:true}).map(animation=>animation.finished));
  });
  return locator.evaluate(lockedDigitFacts);
}

export function assertExhaustedHighlight(before,after,copies) {
  assert.equal(copies,9,'exhausted-digit proof requires all nine actual placements');
  assert.equal(before.highlighted,true,'digit must be locked before exhaustion');
  assert.equal(after.highlighted,true,'exhaustion removed the locked highlight');
  assert.equal(after.exhausted,true,'completed digit lacks its exhausted presentation');
  assert.equal(after.disabled,true,'completed locked digit is still enabled');
  assert.equal(after.background,before.background,'disabled completion lost the locked lime background');
  const rgb=after.background.match(/^rgba?\(\s*([\d.]+)[, ]+\s*([\d.]+)[, ]+\s*([\d.]+)/)??after.background.match(/^color\(srgb\s+([\d.]+)\s+([\d.]+)\s+([\d.]+)/);
  assert.ok(rgb&&Number(rgb[2])>Number(rgb[1])&&Number(rgb[3])<Number(rgb[1]),'locked completion background is not the green/lime palette');
  assert.ok(after.opacity<1||after.color!==before.color,'completed locked digit has no visible disabled distinction');
  return {copies,disabled:true,lime_retained:true,locked:true,before,after};
}

export function elapsedSeconds(text) {
  const fields=text.trim().replace(/^Elapsed\s*/,'').trim().split(':').map(Number);
  assert.ok(fields.length>=2&&fields.length<=3&&fields.every(Number.isFinite),'visible elapsed timer is unavailable');
  return fields.reduce((seconds,value)=>seconds*60+value,0);
}

export function focusDriverCapability(engine) {
  if(engine==='chromium')return {engine,status:'supported',driver:'owned same-context page; Chromium focus emulation disabled'};
  assert.equal(engine,'webkit','unknown browser focus capability');
  return {engine,status:'unsupported',reason:'The public Playwright WebKit driver keeps pages active and focused; bringing another page forward does not deliver blur.',
    required_frontend_regression:'src/pages/home/play-clock.test.ts',native_window_proof:false};
}

// Playwright normally keeps every Chromium page focused. Its public CDP API
// lets us remove that override; ordinary same-context page activation then
// produces browser-generated trusted events while the puzzle stays visible.
export async function chromiumFocusDriver(page) {
  const context=page.context(),session=await context.newCDPSession(page);
  let other;
  const back=async()=>{
    if(other){await other.close();other=undefined}
    await page.bringToFront();
    await page.waitForFunction(()=>document.hasFocus()&&document.visibilityState==='visible',undefined,{timeout:5000});
  };
  try {
    await session.send('Emulation.setFocusEmulationEnabled',{enabled:false});
    await back();
  } catch(error){await session.detach();throw error}
  return {
    async away() {
      assert.equal(other,undefined,'focus target is already active');
      other=await context.newPage();
      const otherSession=await context.newCDPSession(other);
      try {
        await otherSession.send('Emulation.setFocusEmulationEnabled',{enabled:false});
        await other.bringToFront();
        await page.waitForFunction(()=>!document.hasFocus()&&document.visibilityState==='visible',undefined,{timeout:5000});
      } finally {await otherSession.detach()}
    },
    back,
    async close(){try{await back()}finally{await session.detach()}},
  };
}

export function assertFocusClock(facts) {
  assert.equal(facts.before.focused,true,'timer did not start in a focused document');
  for(const boundary of ['before','lost','returned'])assert.equal(facts[boundary].visibility,'visible',`focus proof ${boundary} document is not visible`);
  assert.equal(facts.lost.focused,false,'owned focus target did not blur the puzzle');
  assert.equal(facts.background_end,facts.lost.elapsed,'elapsed timer advanced while unfocused');
  assert.equal(facts.returned.focused,true,'puzzle did not regain focus');
  assert.ok(facts.returned.elapsed<=facts.lost.elapsed+1,'unfocused time was charged when focus returned');
  assert.ok(facts.resumed>=facts.returned.elapsed+2,'elapsed timer did not resume after focus');
  assert.equal(facts.manual_after,facts.manual_before,'focus transition overrode manual pause');
  assert.equal(facts.manual_resume_visible,true,'manual pause was cleared on focus');
  assert.equal(facts.timer_setting_preserved,true,'focus transition changed Show timer preference');
  for(const [type,focused] of [['blur',false],['focus',true]])assert.ok(facts.events?.some(row=>row.type===type&&row.trusted&&row.focused===focused&&row.visibility==='visible'),`no trusted visible ${type} event was observed`);
  return {...facts,scope:'browser-generated focus loss while visible in headless Chromium; native window activation unverified',native_window_proof:false};
}

export function assertFocusCoverage(feedback) {
  assert.equal(feedback.focus_timer.length,1,'one actual Chromium focus receipt is required');
  const [proof]=feedback.focus_timer;
  assert.equal(proof.engine,'chromium','focus receipt must use the qualified Chromium driver');
  assertFocusClock(proof);
  assert.deepEqual(feedback.focus_capabilities,['chromium','webkit'].map(focusDriverCapability),'browser focus capabilities must retain the explicit WebKit limitation');
  return true;
}

// Complete ownership boundary of the authored best-effort tracker. Fields are
// a safe observation projection, not a validation of the complete server DTO.
// Private/admin APIs and every 5xx still fail.
export const telemetryContract={
  source:'frontend/src/lib/analytics-tracker.gny.ts: trackPageView/trackAnalyticsEvent catch ingestion failures; frontend/src/api/analytics.ts: best-effort beacon; docs/native-analytics.md: telemetry must not alter gameplay/navigation',
  channels:[
    {method:'POST',path:'/api/analytics/events',fields:['event_name','product_area','route']},
    {method:'POST',path:'/api/analytics/page-views',fields:['path','referrer']},
  ],
};
const telemetryChannel=failure=>telemetryContract.channels.find(channel=>channel.method===failure.method&&channel.path===failure.path);
const sha256=bytes=>createHash('sha256').update(bytes).digest('hex');

export function classifyFailure(failure) {
  const optional=!!telemetryChannel(failure)&&(failure.kind==='http'&&failure.status>=400&&failure.status<500||failure.kind==='transport');
  return {...failure,blocking:!optional,classification:optional?'best-effort-telemetry':'required-or-unclassified',...(optional?{
    contract:telemetryContract.source,
  }:{})};
}

export function telemetryDimensions(input) {
  // Only the authored low-cardinality dimensions enter evidence. Never retain
  // arbitrary request bodies, board IDs, URLs, tokens or player progress.
  const events=['game_start','game_complete','hint_request','hint_apply','result_copy','result_share','ad_slot_visible','ad_slot_unfilled_visible','ad_slot_filled_visible','ad_slot_optimized_visible','house_ad_ami_visible','house_ad_ami_click'];
  const areas=['home','play','guide','sudoku','suguru','binary','nonogram','futoshiki','shikaku','hashi','calcudoku','kakuro'];
  return {event_name:events.includes(input?.event_name)?input.event_name:'unregistered',product_area:areas.includes(input?.product_area)?input.product_area:'unregistered'};
}

export function publicPathFacts(value,origin) {
  if(typeof value!=='string')return {type:typeof value};
  if(!value)return {type:'string',format:'empty'};
  try {
    const url=new URL(value,origin),parts=url.pathname.split('/').filter(Boolean),games=['sudoku','suguru','binary','nonogram','futoshiki','shikaku','hashi','calcudoku','kakuro'];
    let route='other';
    if(url.origin===origin) {
      if(!parts.length)route='/';
      else if(games.includes(parts[0])&&parts.length<=2)route='/'+parts[0]+(parts.length===2?'/:code':'');
      else if(parts[0]==='how-to-play'&&games.includes(parts[1])&&parts.length===2)route='/how-to-play/'+parts[1];
    }
    return {route,query:!!url.search,fragment:!!url.hash,absolute:/^[a-z][a-z0-9+.-]*:/i.test(value),same_origin:url.origin===origin};
  } catch{return {type:'string',format:'invalid-url'}}
}

export function apiRequestFacts(request,{document,journey,viewport,origin}) {
  const identity={method:request.method(),path:new URL(request.url()).pathname};
  const facts={...identity,initiator:{document:publicPathFacts(document,origin),journey:String(journey).slice(0,100),viewport:{width:viewport?.width,height:viewport?.height}}};
  const channel=telemetryChannel(identity);
  if(!channel)return facts; // Never inspect required/private API payloads.
  const body=request.postDataBuffer();
  if(!body){facts.payload={format:'absent'};return facts}
  const payload=facts.payload={bytes:body.length,sha256:sha256(body)};
  if(body.length>4096){payload.format='over-4KiB';return facts}
  let input;
  try{input=JSON.parse(body.toString('utf8'))}catch{payload.format='invalid-json';return facts}
  if(!input||Array.isArray(input)||typeof input!=='object'){payload.format='non-object-json';return facts}
  Object.assign(payload,{format:'json-object',unrecorded_fields:Object.keys(input).filter(key=>!channel.fields.includes(key)).length,types:Object.fromEntries(channel.fields.map(key=>[key,typeof input[key]]))});
  if(identity.path==='/api/analytics/events')Object.assign(payload,telemetryDimensions(input),{route:publicPathFacts(input.route,origin)});
  else {
    payload.path=publicPathFacts(input.path,origin);
    const value=input.referrer;
    payload.referrer={type:typeof value};
    if(typeof value==='string') {
      if(!value)payload.referrer.format='empty';
      else try{const url=new URL(value);Object.assign(payload.referrer,{format:'url',protocol:['http:','https:'].includes(url.protocol)?url.protocol:'other',same_origin:url.origin===origin,path:url.pathname!=='/',query:!!url.search,fragment:!!url.hash,credentials:!!(url.username||url.password)})}catch{payload.referrer.format='invalid-url'}
    }
  }
  return facts;
}

export async function apiResponseFacts(response,signal) {
  const headers=response.headers(),length=headers['content-length'],encoding=headers['content-encoding'];
  const bytes=/^\d{1,6}$/.test(length??'')?Number(length):null;
  const facts={declared_bytes:bytes,body:'unavailable'};
  // Never fetch arbitrary/unbounded or compressed bodies. Error payloads from
  // this owned local server are short, declared, uncompressed text/JSON.
  if(bytes===null||bytes>1024||encoding&&encoding!=='identity')return {...facts,reason:'undeclared, over-1KiB or encoded'};
  let onAbort;
  try {
    if(signal?.aborted)return {...facts,reason:'diagnostic capture deadline expired'};
    const cancelled=Symbol('capture-cancelled');
    const cancellation=signal&&new Promise(resolve=>{onAbort=()=>resolve(cancelled);signal.addEventListener('abort',onAbort,{once:true})});
    const reading=response.body();
    const body=await (signal?Promise.race([reading,cancellation]):reading);
    if(body===cancelled)return {...facts,reason:'diagnostic capture deadline expired'};
    Object.assign(facts,{bytes:body.length,sha256:sha256(body)});
    if(body.length>1024)return {...facts,reason:'body exceeded declared diagnostic bound'};
    const categories={
      'invalid JSON request body':'invalid-json',
      'request body must contain a single JSON value':'multiple-json-values',
      'request body is too large':'request-too-large',
      'invalid input':'invalid-input',
      'invalid id':'invalid-id',
      'forbidden':'forbidden',
      'unauthorized':'unauthorized',
      'internal server error':'internal-server-error',
    };
    return {...facts,body:'hashed',error_category:categories[body.toString('utf8').trim()]??'unrecognized'};
  } catch{return {...facts,reason:'response body unavailable'}}
  finally{if(onAbort)signal.removeEventListener('abort',onAbort)}
}

// Only actual failing HTTP responses enqueue body enrichment. Drain those
// facts before cleanup; no request/header wait or network-idle delay is added.
export async function closeAfterResponses(owner,pending,timeoutMS) {
  let expired=false,closing;
  const close=()=>closing??=(async()=>owner.close())();
  const abortPending=()=>{for(const controller of pending.values())controller.abort()};
  const drain=async()=>{
    if(!pending.size)return;
    const deadline=setTimeout(()=>{expired=true;abortPending();close().catch(()=>{})},timeoutMS);
    try{while(pending.size){if(expired)abortPending();await Promise.allSettled([...pending.keys()])}}
    finally{clearTimeout(deadline)}
  };
  try {
    await drain();
    await close();
    // A final response can arrive while closing. Its body now either exists
    // or is unavailable; retain that fact before the archive is finalized.
    await drain();
  }
  finally {await close()}
  return {capture_timed_out:expired};
}

// These functions execute read-only in the page and have no captured helpers.
export function stableBoardGeometry(boardSelector) {
  const board=[...document.querySelectorAll(boardSelector)].find(n=>n.getClientRects().length&&getComputedStyle(n).visibility==='visible');
  if(!board)return false;
  const region=board.closest('.player-board-region')??board.parentElement;
  const fields=[board,...region.querySelectorAll('.number-pad,.input-tools,.value-controls,.rectangle-controls,.player-actions')].filter(n=>n.getClientRects().length).map(n=>{
    const r=n.getBoundingClientRect();return [r.x,r.y,r.width,r.height];
  });
  const key=JSON.stringify(fields),same=window.runnerSudokuGeometry===key;
  window.runnerSudokuGeometry=key;
  return same;
}

export function boardDocumentFacts(boardSelector) {
  const visible=node=>node.getClientRects().length&&getComputedStyle(node).visibility==='visible';
  const boards=[...document.querySelectorAll(boardSelector)].filter(visible);
  return {
    path:location.pathname,origin:location.origin,ready:document.readyState,title:document.title.slice(0,80),
    visible_boards:boards.length,cells:boards[0]?.querySelectorAll('[data-cell]').length??0,
    module_scripts:[...document.querySelectorAll('script[type="module"][src]')].slice(0,2).map(node=>{const url=new URL(node.src,location.href);return {origin:url.origin,path:url.pathname.slice(0,120)}}),
    status:[...document.querySelectorAll('[role="status"],[role="alert"]')].filter(visible).slice(0,2).map(node=>node.textContent.trim().slice(0,120)),
  };
}

// A generated-code reopen is a shared prerequisite, not nineteen independent
// fixtures. Its ordinary document/API admission must work without state injection.
export async function reopenSudoku(page,origin,boardPath) {
  let status;
  try {
    const response=await page.goto(origin+boardPath,{waitUntil:'domcontentloaded'});
    status=response?.status();
    assert.ok(status>=200&&status<400,'public Sudoku document did not return success');
    await page.locator(sudokuBoardSelector+' [data-cell="80"]').waitFor({state:'visible'});
    const facts=await page.evaluate(boardDocumentFacts,sudokuBoardSelector);
    assert.equal(facts.path,boardPath,'public reopen changed the admitted puzzle path');
    assert.equal(facts.visible_boards,1,'public reopen must expose one visible Sudoku board');
    assert.equal(facts.cells,81,'public reopen must expose all 81 admitted cells');
    return {http_status:status,...facts};
  } catch(error) {
    let facts;
    try{facts=await page.evaluate(boardDocumentFacts,sudokuBoardSelector)}catch{}
    throw new Error(`public Sudoku reopen: ${JSON.stringify({http_status:status,...facts})}\n${boundedFailure(error,1000)}`);
  }
}

export async function afterReopen(probe,matrix) {
  if(!await probe())return false;
  await matrix();
  return true;
}
