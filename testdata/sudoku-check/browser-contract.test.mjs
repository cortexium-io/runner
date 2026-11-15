import {test} from 'node:test';
import assert from 'node:assert/strict';
import {neighborSelectors,stableBoardGeometry,boardDocumentFacts,reopenSudoku,afterReopen,boundedFailure,sudokuBoardSelector} from '../../cmd/runner-sudoku-check/browser-contract.mjs';

test('Playwright visibility selectors stay outside native DOM geometry queries',()=>{
  const selectors=neighborSelectors('shikaku');
  assert.equal(selectors.board,'.shikaku-workspace:visible .shikaku-board');
  assert.equal(selectors.domBoard,'.shikaku-workspace .shikaku-board');
  const region={querySelectorAll:()=>[]},rect={x:10,y:20,width:300,height:300};
  const board={getClientRects:()=>[rect],getBoundingClientRect:()=>rect,closest:()=>region};
  const hidden={getClientRects:()=>[],getBoundingClientRect:()=>rect,closest:()=>region};
  const previous={document:globalThis.document,window:globalThis.window,getComputedStyle:globalThis.getComputedStyle};
  try {
    globalThis.document={querySelectorAll:selector=>{if(selector.includes(':visible'))throw new SyntaxError('not a native CSS selector');return [hidden,board]}};
    globalThis.window={};globalThis.getComputedStyle=()=>({visibility:'visible'});
    assert.throws(()=>stableBoardGeometry(selectors.board),SyntaxError);
    assert.equal(stableBoardGeometry(selectors.domBoard),false);
    assert.equal(stableBoardGeometry(selectors.domBoard),true);
    rect.width=299;assert.equal(stableBoardGeometry(selectors.domBoard),false);
    assert.equal(stableBoardGeometry(selectors.domBoard),true);
  } finally {
    for(const [name,value] of Object.entries(previous))if(value===undefined)delete globalThis[name];else globalThis[name]=value;
  }
});

test('one failed public reopen blocks all dependent matrix work with its locator and page facts',async()=>{
  let navigation=0,waits=0,matrix=0;
  const facts={path:'/sudoku/sdk6-e-fixture',ready:'complete',visible_boards:0,cells:0,status:['Puzzle could not be prepared.']};
  const page={
    goto:async url=>{navigation++;assert.equal(url,'https://runner.invalid'+facts.path);return {status:()=>200}},
    locator:selector=>{assert.equal(selector,sudokuBoardSelector+' [data-cell="80"]');return {waitFor:async()=>{waits++;throw new Error('locator.waitFor: timeout\nCall log:\n  waiting for the admitted board to become visible')}}},
    evaluate:async(fn,selector)=>{assert.equal(fn,boardDocumentFacts);assert.equal(selector,sudokuBoardSelector);return facts},
  };
  let failure;
  const ran=await afterReopen(async()=>{try{await reopenSudoku(page,'https://runner.invalid',facts.path);return true}catch(error){failure=error;return false}},async()=>{matrix++});
  assert.equal(ran,false);assert.equal(navigation,1);assert.equal(waits,1);assert.equal(matrix,0);
  assert.match(failure.message,/"http_status":200/);assert.match(failure.message,/"cells":0/);
  assert.match(failure.message,/Puzzle could not be prepared/);assert.match(failure.message,/Call log:[\s\S]*admitted board/);
});

test('page diagnostics bind the actual document and module origin without dumping its body',()=>{
  const board={getClientRects:()=>[{}],querySelectorAll:()=>Array.from({length:81},()=>({}))};
  const status={getClientRects:()=>[{}],textContent:'Canonical admission pending'};
  const previous={document:globalThis.document,location:globalThis.location,getComputedStyle:globalThis.getComputedStyle};
  try {
    globalThis.location={pathname:'/sudoku/sdk6-e-fixture',origin:'https://runner.invalid',href:'https://runner.invalid/sudoku/sdk6-e-fixture'};
    globalThis.getComputedStyle=()=>({visibility:'visible'});
    globalThis.document={readyState:'complete',title:'Sudoku fixture',querySelectorAll:selector=>selector===sudokuBoardSelector?[board]:selector.startsWith('script')?[{src:'/assets/fixture.mjs'}]:[status]};
    const facts=boardDocumentFacts(sudokuBoardSelector);
    assert.equal(facts.cells,81);assert.equal(facts.origin,'https://runner.invalid');
    assert.deepEqual(facts.module_scripts,[{origin:'https://runner.invalid',path:'/assets/fixture.mjs'}]);
    assert.deepEqual(facts.status,['Canonical admission pending']);assert.equal('body' in facts,false);
  } finally {
    for(const [name,value] of Object.entries(previous))if(value===undefined)delete globalThis[name];else globalThis[name]=value;
  }
});

test('successful reopen runs the matrix once; HTTP failure never waits for a nonexistent board',async()=>{
  let matrix=0,waits=0;
  const facts={path:'/sudoku/sdk6-e-fixture',visible_boards:1,cells:81};
  const page={goto:async()=>({status:()=>200}),locator:()=>({waitFor:async()=>{waits++}}),evaluate:async()=>facts};
  assert.equal(await afterReopen(async()=>{const proof=await reopenSudoku(page,'https://runner.invalid',facts.path);assert.equal(proof.cells,81);return true},async()=>{matrix++}),true);
  assert.equal(matrix,1);assert.equal(waits,1);
  page.goto=async()=>({status:()=>404});
  await assert.rejects(reopenSudoku(page,'https://runner.invalid',facts.path),/"http_status":404/);
  assert.equal(waits,1);
});

test('bounded diagnostics retain the Playwright call log instead of only the first line',()=>{
  const message='locator.tap: timeout\nCall log:\n  element receives pointer events but never becomes stable\n';
  assert.match(boundedFailure(new Error(message)),/Call log:[\s\S]*never becomes stable/);
  assert.equal(boundedFailure(new Error('x'.repeat(5000))).length,1400);
  assert.equal(boundedFailure('\u001b[31mfailed\u001b[0m'),'failed');
});
