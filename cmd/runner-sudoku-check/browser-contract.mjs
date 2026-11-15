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
