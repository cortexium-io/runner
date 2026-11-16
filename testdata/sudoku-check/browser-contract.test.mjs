import {test} from 'node:test';
import assert from 'node:assert/strict';
import {neighborSelectors,stableBoardGeometry,boardDocumentFacts,reopenSudoku,afterReopen,boundedFailure,recordFailure,sudokuBoardSelector,activateTarget,targetFacts,interactionFacts,savedBoardAction,newSudokuFromPlay,classifyFailure,telemetryContract,telemetryDimensions,apiRequestFacts,apiResponseFacts,closeAfterResponses,neighborGeometry,assertFeedbackLayout,assertExhaustedHighlight,elapsedSeconds,focusDriverCapability,chromiumFocusDriver,assertFocusClock,assertFocusCoverage} from '../../cmd/runner-sudoku-check/browser-contract.mjs';

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

function actionFixture({matches=1,enabled=true,inViewport=true,hit=true}={}) {
  const events=[],observed=[];
  const facts={path:'/sudoku/sdk6-e-fixture',viewport:{width:390,height:844},scroll:{x:0,y:0},selected:'0',focus:{tag:'BUTTON',cell:'0'},board:{x:10,y:100,width:300,height:300}};
  const target={tag:'BUTTON',label:'Undo',enabled,in_viewport:inViewport,hit,centre:{x:50,y:420},cover:{tag:'BUTTON',label:hit?'Undo':'Options'}};
  const locator={toString:()=>"locator('.input-tools button').filter({ hasText: 'Undo' })",count:async()=>matches,waitFor:async()=>{},evaluate:async fn=>{
    if(fn===targetFacts){events.push('hit-test');return structuredClone(target)}
    events.push('scroll');facts.scroll.y=200;facts.board.y=-100;target.in_viewport=true;
  }};
  const point=async(x,y)=>{assert.deepEqual({x,y},target.centre);events.push('pointer');facts.focus={tag:'BUTTON',label:'Undo'}};
  const page={evaluate:async fn=>fn===interactionFacts?structuredClone(facts):events.push('layout'),mouse:{move:async()=>{events.push('move')},click:point},touchscreen:{tap:point}};
  return {page,locator,target,facts,events,observed,settings:{observe:row=>observed.push(row)}};
}

test('target facts distinguish disabled pointer-transparent layout from an actionable control',()=>{
  const cover={tagName:'DIV',textContent:'',getAttribute:()=>null};
  const node={tagName:'BUTTON',textContent:'Undo',getAttribute:()=>null,matches:selector=>{assert.equal(selector,':disabled');return true},contains:other=>other===node,getBoundingClientRect:()=>({x:10,y:20,width:60,height:40})};
  const previous={document:globalThis.document,innerWidth:globalThis.innerWidth,innerHeight:globalThis.innerHeight};
  try {
    globalThis.document={elementFromPoint:()=>cover};globalThis.innerWidth=390;globalThis.innerHeight=844;
    const facts=targetFacts(node);assert.equal(facts.enabled,false);assert.equal(facts.hit,false);assert.equal(facts.in_viewport,true);
    assert.deepEqual(facts.centre,{x:40,y:40});assert.equal(facts.cover.tag,'DIV');assert.equal(facts.label,'Undo');
  } finally {
    for(const [name,value] of Object.entries(previous))if(value===undefined)delete globalThis[name];else globalThis[name]=value;
  }
});

test('one action contract refuses missing, ambiguous, disabled, offscreen and covered targets with bounded context',async()=>{
  for(const [settings,phase,reason] of [
    [{matches:0},'locate','one actual UI target'],[{matches:2},'locate','one actual UI target'],
    [{enabled:false},'hit-test','disabled'],[{inViewport:false},'hit-test','offscreen'],[{hit:false},'hit-test','covered'],
  ]) {
    const f=actionFixture(settings);
    await assert.rejects(activateTarget(f.page,f.locator,true,f.settings),error=>{
      assert.match(error.message,new RegExp(reason));assert.match(error.message,/input-tools/);
      assert.equal(error.interaction.phase,phase);assert.deepEqual(error.interaction.before.viewport,{width:390,height:844});
      assert.equal(error.interaction.after.selected,'0');assert.equal(error.interaction.before.focus.cell,'0');
      return true;
    });
    assert.equal(f.events.includes('pointer'),false);assert.equal(f.events.includes('scroll'),false);
    assert.equal(f.observed.length,1);
  }
});

test('ordinary secondary scrolling precedes a fresh hit-test and trusted input and preserves transition facts',async()=>{
  for(const touch of [true,false]) {
    const f=actionFixture({inViewport:false});
    await activateTarget(f.page,f.locator,touch,{...f.settings,secondary:true});
    assert.deepEqual(f.events,touch?['scroll','layout','hit-test','pointer','layout']:['scroll','layout','hit-test','move','layout','hit-test','pointer','layout']);
    const proof=f.observed[0];assert.equal(proof.phase,'complete');
    assert.equal(proof.before.scroll.y,0);assert.equal(proof.scrolled.scroll.y,200);
    assert.equal(proof.scrolled.board.y,-100);assert.equal(proof.after.focus.label,'Undo');
  }
});

test('mouse movement renders ordinary prior-hover dismissal before the fresh hit-test',async()=>{
  const f=actionFixture({hit:false});
  f.target.cover={tag:'DIV',label:'Previous digit hover helper'};
  f.page.mouse.move=async(x,y)=>{
    assert.deepEqual({x,y},f.target.centre);f.events.push('move');
    f.target.hit=true;f.target.cover={tag:'BUTTON',label:'Undo'};f.target.centre={x:x+5,y:y+5};
  };
  await activateTarget(f.page,f.locator,false,f.settings);
  assert.deepEqual(f.events,['hit-test','move','layout','hit-test','pointer','layout']);
  const evidence=f.observed[0];
  assert.equal(evidence.pointer_before.hit,false);assert.equal(evidence.pointer_before.cover.label,'Previous digit hover helper');
  assert.notDeepEqual(evidence.target.centre,evidence.pointer_before.centre);
  assert.equal(evidence.target.hit,true);assert.equal(evidence.phase,'complete');
});

test('mouse preparation still rejects persistent overlays and offscreen targets without clicking',async()=>{
  for(const settings of [{hit:false},{inViewport:false}]) {
    const f=actionFixture(settings);
    await assert.rejects(activateTarget(f.page,f.locator,false,f.settings),settings.hit===false?/centre is covered/:/centre is offscreen/);
    assert.equal(f.events.includes('pointer'),false);
    assert.equal(f.events.includes('move'),settings.inViewport!==false);
    assert.equal(f.observed[0].phase,'hit-test');
    assert.equal(f.observed[0].pointer_before.hit,settings.hit!==false);
  }
});

test('home continuation targets the admitted board and accepts its authored unloaded-save action',()=>{
  const identities=[],names=[];
  const page={locator:selector=>{identities.push(selector);return {getByRole:(role,options)=>{assert.equal(role,'button');names.push(options.name);return options}}}};
  savedBoardAction(page,'sudoku:sdk7-9-9-easy-fixture');
  assert.equal(identities[0],'.saved-row[data-board-id="sudoku:sdk7-9-9-easy-fixture"]');
  assert.ok(names[0].test('Resume Sudoku easy 9 by 9'));
  assert.ok(names[0].test('Open to check Sudoku easy 9 by 9'));
  assert.equal(names[0].test('View board Sudoku easy 9 by 9'),false);
  savedBoardAction(page,'sudoku:sdk7-9-9-easy-fixture',{complete:true});
  assert.ok(names[1].test('View board Sudoku easy 9 by 9'));
  assert.equal(names[1].test('Resume Sudoku easy 9 by 9'),false);
  assert.throws(()=>savedBoardAction(page,'bad" selector'),/invalid admitted/);
});

test('in-play New follows its actual visible action or Options after More actions removal',async()=>{
  for(const setup of [{header:true,standalone:false},{header:false,standalone:true},{header:false,standalone:false}]) {
    let open=false;const actions=[];
    const header={id:'header',isVisible:async()=>setup.header},summary={id:'summary'};
    const target={id:'secondary',count:async()=>setup.standalone||open?1:0};
    const role=(name,options)=>{assert.equal(name,'button');assert.deepEqual(options,{name:'New puzzle',exact:true})};
    const settings={count:async()=>1,getAttribute:async name=>{assert.equal(name,'open');return open?'':null}};
    const page={locator:selector=>{
      if(selector==='.sudoku-play .play-topbar')return {getByRole:(name,options)=>{role(name,options);return header}};
      if(selector==='.sudoku-play')return {getByRole:(name,options)=>{role(name,options);return target}};
      if(selector==='.sudoku-play .play-settings')return settings;
      assert.equal(selector,'.sudoku-play .play-settings > summary');return summary;
    }};
    await newSudokuFromPlay(page,async selected=>{actions.push(selected.id);if(selected===summary)open=true});
    assert.deepEqual(actions,setup.header?['header']:setup.standalone?['secondary']:['summary','secondary']);
  }
});

test('in-play New refuses absent or ambiguous actual controls',async()=>{
  for(const count of [0,2]) {
    const page={locator:selector=>{
      if(selector.endsWith('.play-topbar'))return {getByRole:()=>({isVisible:async()=>false})};
      if(selector==='.sudoku-play')return {getByRole:()=>({count:async()=>count})};
      return {count:async()=>0};
    }};
    let actions=0;
    await assert.rejects(newSudokuFromPlay(page,async()=>{actions++}),count===0?/one Options disclosure/:/one visible in-play New/);
    assert.equal(actions,0);
  }
});

function feedbackLayout(){return {
  board:{x:20,y:100,width:350,right:370},frame:{x:10,y:90,width:370,right:380},primary_height:60,
  header:{x:10,y:40,width:370,height:50,right:380,bottom:90},header_count:1,header_content:{x:10,y:40,right:380,bottom:90},
  actions:[{label:'Check',count:1,rect:{x:220,y:50,width:70,height:32,right:290,bottom:82},font:14,inside_primary:false,inside_header:true,target:{enabled:true,in_viewport:true,hit:true}},
    {label:'Restart',count:1,rect:{x:300,y:50,width:80,height:32,right:380,bottom:82},font:14,inside_primary:false,inside_header:true,target:{enabled:true,in_viewport:true,hit:true}}],
  repeated_entry:false,more_actions:false,unlock_highlight:false,lock_prose:false,
}}

test('owner-feedback acceptance requires removed UI and separate top-right secondary actions',()=>{
  assert.equal(assertFeedbackLayout(feedbackLayout()).compact_right_board_actions,true);
  for(const removed of ['repeated_entry','more_actions','unlock_highlight','lock_prose']) {
    const facts=feedbackLayout();facts[removed]=true;assert.throws(()=>assertFeedbackLayout(facts),new RegExp(removed));
  }
  const below=feedbackLayout();below.actions[0].rect.bottom=110;assert.throws(()=>assertFeedbackLayout(below),/above the board/);
  const primary=feedbackLayout();primary.actions[1].inside_primary=true;assert.throws(()=>assertFeedbackLayout(primary),/separate compact/);
  const missing=feedbackLayout();missing.actions=[];assert.throws(()=>assertFeedbackLayout(missing),/measurements/);
});

test('right-side action grouping uses the play header rather than each button or the fitted board edge',()=>{
  const fitted=feedbackLayout();
  fitted.board=fitted.frame={x:10.890625,y:100,width:298.21875,right:309.109375};
  fitted.header_content={x:8,y:40,right:312,bottom:90};
  fitted.actions[0].rect={x:174,y:50,width:62,height:32,right:236,bottom:82};
  fitted.actions[1].rect={x:242,y:50,width:70,height:32,right:312,bottom:82};
  assert.equal(assertFeedbackLayout(fitted).action_group.right,312);
  const crossing=feedbackLayout();
  crossing.board=crossing.frame={x:10,y:100,width:300,right:310};
  crossing.header_content={x:10,y:40,right:310,bottom:90};
  crossing.actions[0].rect={x:140,y:50,width:80,height:32,right:220,bottom:82};
  crossing.actions[1].rect={x:230,y:50,width:80,height:32,right:310,bottom:82};
  assert.equal(assertFeedbackLayout(crossing).action_group.x,140);
  const centered=feedbackLayout();
  centered.actions[0].rect={x:120,y:50,width:70,height:32,right:190,bottom:82};
  centered.actions[1].rect={x:190,y:50,width:80,height:32,right:270,bottom:82};
  assert.throws(()=>assertFeedbackLayout(centered),/group is not on the right/);
  const left=feedbackLayout();
  for(const action of left.actions){action.rect.x-=180;action.rect.right-=180}
  assert.throws(()=>assertFeedbackLayout(left),/group is not on the right/);
});

test('secondary action grouping rejects unrelated, clipped, overlapping, reversed or unusable targets',()=>{
  const unrelated=feedbackLayout();unrelated.actions[0].inside_header=false;
  assert.throws(()=>assertFeedbackLayout(unrelated),/outside the play header/);
  const outside=feedbackLayout();outside.header_content.right=370;
  assert.throws(()=>assertFeedbackLayout(outside),/outside the usable play header/);
  const overlap=feedbackLayout();overlap.actions[1].rect.x=280;overlap.actions[1].rect.right=360;
  assert.throws(()=>assertFeedbackLayout(overlap),/overlap or are out of order/);
  const reversed=feedbackLayout();[reversed.actions[0].rect,reversed.actions[1].rect]=[reversed.actions[1].rect,reversed.actions[0].rect];
  assert.throws(()=>assertFeedbackLayout(reversed),/overlap or are out of order/);
  const wrapped=feedbackLayout();wrapped.board.y=180;wrapped.actions[1].rect.y=90;wrapped.actions[1].rect.bottom=122;
  assert.throws(()=>assertFeedbackLayout(wrapped),/horizontal action row/);
  const offscreen=feedbackLayout();offscreen.actions[0].target.in_viewport=false;
  assert.throws(()=>assertFeedbackLayout(offscreen),/target is offscreen/);
  const covered=feedbackLayout();covered.actions[0].target.hit=false;
  assert.throws(()=>assertFeedbackLayout(covered),/target is covered/);
  covered.actions[0].target.enabled=false;
  assert.equal(assertFeedbackLayout(covered).compact_right_board_actions,true);
});

test('the shared failure recorder preserves measured layout and existing interaction facts on rejection only',()=>{
  const facts=feedbackLayout();facts.header_content.right=370;
  let failure;
  try{assertFeedbackLayout(facts)}catch(error){failure=error}
  const row={};recordFailure(row,failure);
  assert.match(row.failure,/outside the usable play header/);
  assert.deepEqual(row.feedback_layout.header_content,facts.header_content);
  assert.deepEqual(row.feedback_layout.action_group,{x:220,y:50,right:380,bottom:82});
  assert.deepEqual(row.feedback_layout.actions,facts.actions);
  const interaction={phase:'hit-test',target:{hit:false}};
  const pointer=Error('covered');pointer.interaction=interaction;
  const pointerRow={};recordFailure(pointerRow,pointer);
  assert.equal(pointerRow.interaction,interaction);assert.equal('feedback_layout' in pointerRow,false);
});

test('a locked completed digit must retain lime while visibly and semantically disabled',()=>{
  const before={highlighted:true,disabled:false,exhausted:false,background:'rgb(221, 231, 179)',color:'rgb(30, 40, 50)',opacity:1};
  const after={...before,disabled:true,exhausted:true,opacity:0.65};
  assert.equal(assertExhaustedHighlight(before,after,9).lime_retained,true);
  assert.throws(()=>assertExhaustedHighlight(before,{...after,disabled:false},9),/still enabled/);
  assert.throws(()=>assertExhaustedHighlight(before,{...after,background:'rgb(240, 240, 240)'},9),/lost the locked lime/);
  assert.throws(()=>assertExhaustedHighlight(before,{...after,opacity:1},9),/visible disabled/);
  assert.throws(()=>assertExhaustedHighlight(before,after,8),/all nine/);
});

function focusFacts() {
  return {before:{focused:true,visibility:'visible',elapsed:3},lost:{focused:false,visibility:'visible',elapsed:3},background_end:3,
    returned:{focused:true,visibility:'visible',elapsed:3},resumed:5,manual_before:5,manual_after:5,manual_resume_visible:true,timer_setting_preserved:true,
    events:[{type:'blur',trusted:true,focused:false,visibility:'visible'},{type:'focus',trusted:true,focused:true,visibility:'visible'}]};
}

test('focus-clock acceptance requires actual trusted visible transitions and rejects charged time or state loss',()=>{
  assert.equal(elapsedSeconds('Elapsed 1:23'),83);assert.equal(elapsedSeconds('1:02:03'),3723);
  const facts=focusFacts();
  assert.match(assertFocusClock(facts).scope,/while visible/);
  assert.throws(()=>assertFocusClock({...facts,lost:{...facts.lost,visibility:'hidden'}}),/document is not visible/);
  assert.throws(()=>assertFocusClock({...facts,events:[]}),/no trusted visible blur/);
  assert.throws(()=>assertFocusClock({...facts,events:facts.events.map(row=>({...row,trusted:false}))}),/no trusted visible blur/);
  assert.throws(()=>assertFocusClock({...facts,events:[facts.events[0]]}),/no trusted visible focus/);
  assert.throws(()=>assertFocusClock({...facts,background_end:8}),/advanced while unfocused/);
  assert.throws(()=>assertFocusClock({...facts,returned:{...facts.returned,elapsed:8}}),/charged when focus returned/);
  assert.throws(()=>assertFocusClock({...facts,resumed:3}),/did not resume/);
  assert.throws(()=>assertFocusClock({...facts,manual_after:7}),/overrode manual pause/);
  assert.throws(()=>assertFocusClock({...facts,timer_setting_preserved:false}),/Show timer/);
});

test('focus coverage distinguishes the known WebKit capability gap from missing or failed Chromium proof',()=>{
  const feedback={focus_timer:[{engine:'chromium',...focusFacts()}],focus_capabilities:['chromium','webkit'].map(focusDriverCapability)};
  assert.equal(assertFocusCoverage(feedback),true);
  assert.equal(feedback.focus_capabilities[1].status,'unsupported');
  assert.equal(feedback.focus_capabilities[1].required_frontend_regression,'src/pages/home/play-clock.test.ts');
  assert.throws(()=>focusDriverCapability('unknown'),/unknown browser/);
  assert.throws(()=>assertFocusCoverage({...feedback,focus_timer:[]}),/actual Chromium focus receipt/);
  assert.throws(()=>assertFocusCoverage({...feedback,focus_timer:[{engine:'webkit',...focusFacts()}]}),/qualified Chromium driver/);
  assert.throws(()=>assertFocusCoverage({...feedback,focus_timer:[{...feedback.focus_timer[0],lost:{focused:true,visibility:'visible'}}]}),/did not blur/);
  assert.throws(()=>assertFocusCoverage({...feedback,focus_capabilities:[feedback.focus_capabilities[0],{engine:'webkit',status:'failed'}]}),/explicit WebKit limitation/);
});

test('focus driver setup errors propagate and detach the owned session',async()=>{
  let detached=0;
  const page={context:()=>({newCDPSession:async()=>({send:async()=>{throw Error('CDP unavailable')},detach:async()=>{detached++}})})};
  await assert.rejects(chromiumFocusDriver(page),/CDP unavailable/);
  assert.equal(detached,1);
});

test('the complete owned tracker contract keeps both POST 4xx visible while unknown/private errors and all 5xx block',()=>{
  assert.deepEqual(telemetryContract.channels.map(c=>c.path),['/api/analytics/events','/api/analytics/page-views']);
  for(const {path} of telemetryContract.channels) {
    const optional=classifyFailure({kind:'http',method:'POST',path,status:400});
    assert.equal(optional.blocking,false);assert.match(optional.contract,/analytics-tracker/);assert.match(optional.contract,/native-analytics/);
    assert.equal(optional.status,400);assert.equal(optional.path,path);
    assert.equal(classifyFailure({kind:'http',method:'POST',path,status:500}).blocking,true);
    assert.equal(classifyFailure({kind:'transport',method:'POST',path}).blocking,false);
  }
  for(const failure of [
    {kind:'http',method:'POST',path:'/api/games/admit',status:400},
    {kind:'http',method:'POST',path:'/api/unknown',status:404},
    {kind:'http',method:'POST',path:'/api/analytics/events',status:500},
    {kind:'http',method:'GET',path:'/api/analytics/events',status:400},
    {kind:'http',method:'POST',path:'/api/analytics/private',status:400},
    {kind:'http',method:'POST',path:'/api/admin/analytics',status:403},
    {kind:'transport',method:'POST',path:'/api/games/admit'},
    {kind:'pageerror',message:'uncaught error'},
  ])assert.equal(classifyFailure(failure).blocking,true);
  assert.deepEqual(telemetryDimensions({event_name:'hint_apply',product_area:'sudoku',token:'secret',route:'/sudoku/private',snapshot:{entries:[1]}}),{event_name:'hint_apply',product_area:'sudoku'});
  assert.deepEqual(telemetryDimensions({event_name:'secret-identifier',product_area:'secret-identifier'}),{event_name:'unregistered',product_area:'unregistered'});
});

test('request snapshots preserve initiating document/journey while redacting codes, queries and full referrers',()=>{
  const origin='https://runner.invalid',context={origin,document:origin+'/sudoku/private-board?token=private-query#private-fragment',journey:'appearance-controls',viewport:{width:390,height:844}};
  const body=Buffer.from(JSON.stringify({path:'/sudoku/private-board?secret=private-query',referrer:'https://alice:password@private-referrer.invalid/private-path?secret=private-query#private-fragment',unexpected:'private-payload'}));
  const request={method:()=> 'POST',url:()=>origin+'/api/analytics/page-views',postDataBuffer:()=>body};
  const facts=apiRequestFacts(request,context);
  context.document=origin+'/';context.journey='next-journey';context.viewport.width=1440;
  assert.equal(facts.initiator.journey,'appearance-controls');assert.equal(facts.initiator.viewport.width,390);
  assert.equal(facts.initiator.document.route,'/sudoku/:code');assert.equal(facts.initiator.document.query,true);
  assert.equal(facts.payload.path.route,'/sudoku/:code');assert.equal(facts.payload.unrecorded_fields,1);
  assert.deepEqual(facts.payload.referrer,{type:'string',format:'url',protocol:'https:',same_origin:false,path:true,query:true,fragment:true,credentials:true});
  assert.equal(facts.payload.bytes,body.length);assert.match(facts.payload.sha256,/^[a-f0-9]{64}$/);
  assert.doesNotMatch(JSON.stringify(facts),/private-board|private-query|private-fragment|private-referrer|private-payload|alice|password/);
  request.url=()=>origin+'/api/admin/analytics';request.postDataBuffer=()=>{throw Error('private API payload must not be inspected')};
  assert.equal(apiRequestFacts(request,context).payload,undefined);
});

test('telemetry payload diagnostics bound parsing and retain malformed bodies only as size/hash',()=>{
  const context={origin:'https://runner.invalid',document:'https://runner.invalid/',journey:'normal-ui-admission',viewport:{width:390,height:844}};
  const request={method:()=> 'POST',url:()=>context.origin+'/api/analytics/events',postDataBuffer:()=>Buffer.from('private-malformed-json')};
  assert.equal(apiRequestFacts(request,context).payload.format,'invalid-json');
  request.postDataBuffer=()=>Buffer.alloc(4097,120);
  const large=apiRequestFacts(request,context).payload;assert.equal(large.format,'over-4KiB');assert.equal(large.bytes,4097);
  request.postDataBuffer=()=>Buffer.from(JSON.stringify({event_name:'hint_apply',product_area:'sudoku',route:'/',token:'private-token'}));
  const event=apiRequestFacts(request,context).payload;
  assert.equal(event.event_name,'hint_apply');assert.equal(event.product_area,'sudoku');assert.equal(event.unrecorded_fields,1);
  assert.doesNotMatch(JSON.stringify(event),/private-token|"token"/);
});

test('bounded response facts distinguish known decode/domain errors without retaining arbitrary body text',async()=>{
  for(const [text,category] of [['invalid JSON request body\n','invalid-json'],['invalid input\n','invalid-input'],['private-response-secret','unrecognized']]) {
    const body=Buffer.from(text),response={headers:()=>({'content-length':String(body.length)}),body:async()=>body};
    const facts=await apiResponseFacts(response);
    assert.equal(facts.error_category,category);assert.equal(facts.bytes,body.length);assert.match(facts.sha256,/^[a-f0-9]{64}$/);
    assert.doesNotMatch(JSON.stringify(facts),/private-response-secret/);
  }
  for(const headers of [{},{'content-length':'1025'},{'content-length':'20','content-encoding':'gzip'}]) {
    const facts=await apiResponseFacts({headers:()=>headers,body:async()=>{assert.fail('unbounded/encoded response must not be fetched')}});
    assert.equal(facts.body,'unavailable');
  }
  const unavailable=await apiResponseFacts({headers:()=>({'content-length':'12'}),body:async()=>{throw Error('private error detail')}});
  assert.equal(unavailable.body,'unavailable');assert.doesNotMatch(JSON.stringify(unavailable),/private error detail/);
});

test('response enrichment settles before close and retains late close-time facts before returning',async()=>{
  const pending=new Map(),events=[];let resolveFirst,resolveLate,closeStarted;
  const first=new Promise(resolve=>{resolveFirst=resolve}).then(()=>events.push('first-facts'));
  pending.set(first,new AbortController());first.then(()=>pending.delete(first));
  const late=new Promise(resolve=>{resolveLate=resolve}).then(()=>events.push('late-facts'));
  const started=new Promise(resolve=>{closeStarted=resolve});
  const owner={close:async()=>{events.push('close');pending.set(late,new AbortController());late.then(()=>pending.delete(late));closeStarted()}};
  const closing=closeAfterResponses(owner,pending,12000);
  assert.deepEqual(events,[]);resolveFirst();await started;
  assert.deepEqual(events,['first-facts','close']);resolveLate();await closing;
  assert.deepEqual(events,['first-facts','close','late-facts']);assert.equal(pending.size,0);
});

test('optional body timeout remains visible without rejecting gameplay; real cleanup errors still propagate',async()=>{
  const pending=new Map(),controller=new AbortController();let release,closed=0;
  const response={headers:()=>({'content-length':'12'}),body:()=>new Promise(resolve=>{release=resolve})};
  let retained,lateRetained;
  const work=apiResponseFacts(response,controller.signal).then(facts=>{retained=facts});pending.set(work,controller);work.then(()=>pending.delete(work));
  const facts=await closeAfterResponses({close:async()=>{
    closed++;
    const lateController=new AbortController(),lateResponse={headers:response.headers,body:()=>new Promise(()=>{})};
    const late=apiResponseFacts(lateResponse,lateController.signal).then(facts=>{lateRetained=facts});pending.set(late,lateController);late.then(()=>pending.delete(late));
  }},pending,1);
  assert.equal(facts.capture_timed_out,true);assert.equal(retained.body,'unavailable');assert.equal(retained.reason,'diagnostic capture deadline expired');
  assert.equal(lateRetained.reason,'diagnostic capture deadline expired');
  assert.equal(closed,1);assert.equal(pending.size,0);
  const saved=structuredClone(retained);release(Buffer.from('invalid input\n'));await Promise.resolve();await Promise.resolve();
  assert.deepEqual(retained,saved,'late body completion overwrote the unavailable diagnostic');
  await assert.rejects(closeAfterResponses({close:async()=>{throw Error('real cleanup failure')}},new Map(),12000),/real cleanup failure/);
});

test('neighbor measurement omits hidden/zero-area nodes and retains only primary controls plus Undo',()=>{
  const make=(label,r,visibility='visible')=>({textContent:label,getBoundingClientRect:()=>r,getClientRects:()=>[r],visibility});
  const visible=make('Undo',{x:10,y:950,width:60,height:40}),zero=make('hidden',{x:0,y:0,width:0,height:0}),hidden=make('hidden',{x:10,y:10,width:60,height:40},'hidden');
  const frame=make('',{x:0,y:0,width:300,height:300}),region={querySelectorAll:selector=>{
    assert.ok(selector.includes('[data-action="undo"]'));assert.equal(selector.includes('.player-actions button'),false);return [visible,zero,hidden];
  }};
  const board={...frame,closest:selector=>selector==='.player-board-frame'?frame:region,scrollWidth:300,scrollHeight:300,clientWidth:300,clientHeight:300};
  const previous={document:globalThis.document,getComputedStyle:globalThis.getComputedStyle,innerWidth:globalThis.innerWidth,innerHeight:globalThis.innerHeight};
  try {
    globalThis.document={documentElement:{scrollWidth:1440}};globalThis.getComputedStyle=node=>({visibility:node.visibility});globalThis.innerWidth=1440;globalThis.innerHeight=900;
    const m=neighborGeometry(board);assert.equal(m.controls.length,1);assert.equal(m.controls[0].label,'Undo');assert.equal(m.controls[0].y,950);
  } finally {
    for(const [name,value] of Object.entries(previous))if(value===undefined)delete globalThis[name];else globalThis[name]=value;
  }
});
