import assert from 'node:assert/strict';

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
  // In-progress touch play keeps New secondary; desktop and solved play can
  // expose it in the heading. Follow the actual affordance, never a viewport
  // guess or an unrelated New button in a dialog.
  const more=page.locator('.sudoku-play .more-puzzle-actions');
  assert.equal(await more.count(),1,'one in-play More actions disclosure is required');
  if(await more.getAttribute('open')===null)await activate(page.locator('.sudoku-play .more-puzzle-actions > summary'));
  await activate(more.getByRole('button',{name:'New puzzle',exact:true}));
}

export function classifyFailure(failure) {
  const optional=failure.kind==='http'&&failure.method==='POST'&&failure.path==='/api/analytics/events'&&failure.status>=400&&failure.status<500;
  return {...failure,blocking:!optional,classification:optional?'best-effort-telemetry':'required-or-unclassified',...(optional?{
    contract:'frontend/src/lib/analytics-tracker.gny.ts: trackAnalyticsEvent catches ingestion failures; frontend/src/pages/home/analytics.test.ts asserts gameplay despite failed telemetry',
  }:{})};
}

export function telemetryDimensions(input) {
  // Only the authored low-cardinality dimensions enter evidence. Never retain
  // arbitrary request bodies, board IDs, URLs, tokens or player progress.
  const events=['game_start','game_complete','hint_request','hint_apply','result_copy','result_share','ad_slot_visible','ad_slot_unfilled_visible','ad_slot_filled_visible','ad_slot_optimized_visible','house_ad_ami_visible','house_ad_ami_click'];
  const areas=['home','play','guide','sudoku','suguru','binary','nonogram','futoshiki','shikaku','hashi','calcudoku','kakuro'];
  return {event_name:events.includes(input?.event_name)?input.event_name:'unregistered',product_area:areas.includes(input?.product_area)?input.product_area:'unregistered'};
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
