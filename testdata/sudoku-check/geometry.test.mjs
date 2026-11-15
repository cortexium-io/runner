import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {assertGeometry,assertNeighborGeometry,viewports,measure} from '../../cmd/runner-sudoku-check/geometry.mjs';

const fixtures=JSON.parse(await readFile(new URL('./layouts.json',import.meta.url),'utf8'));
function metrics(name) {
  const f=structuredClone(fixtures[name]),p=f.pad,t=f.tools;
  const key={font:30,enabled:true,hit:true,textOverflow:false,iconWidth:24,captionFont:12};
  return {
    viewport:{width:f.viewport.width,height:f.viewport.height},documentWidth:f.viewport.width,
    board:f.board,frame:f.frame,controls:f.controls,
    internalWidth:f.board.width,internalHeight:f.board.height,clientWidth:f.board.width,clientHeight:f.board.height,
    digits:Array.from({length:9},(_,i)=>({...key,value:i+1,x:p.x+(i%p.columns)*(p.width+p.gap),y:p.y+Math.floor(i/p.columns)*(p.height+p.gap),width:p.width,height:p.height})),
    tools:['Undo','Erase','Notes','Hint'].map((name,i)=>({...key,name,x:t.x+i*(t.width+t.gap),y:t.y,width:t.width,height:t.height})),
    permanentModeControls:0,desktopPrimaryBeside:false,
  };
}

test('agreed phone row, intact landscape panels and short desktop scrolling are distinct contracts',()=>{
  assert.equal(assertGeometry(metrics('portrait'),fixtures.portrait.viewport).composition,'under-board');
  assert.equal(assertGeometry(metrics('landscape'),fixtures.landscape.viewport).composition,'touch-side-panels');
  assert.equal(assertGeometry(metrics('desktop'),fixtures.desktop.viewport).scrollingRequired,true);
});

test('old portrait 3×3 and desktop Duo composition cannot pass on target sizes alone',()=>{
  const phone=metrics('portrait');
  phone.digits.forEach((key,i)=>{key.x=10+(i%3)*90;key.y=390+Math.floor(i/3)*20;key.height=24});
  assert.throws(()=>assertGeometry(phone,fixtures.portrait.viewport),/one deliberate row|overlap/);
  const desktop=metrics('desktop');desktop.desktopPrimaryBeside=true;
  assert.throws(()=>assertGeometry(desktop,fixtures.desktop.viewport),/Duo/);
});

test('four-tool grouping, matching large footprints and ordered 3×3 are independently enforced',()=>{
  for(const mutate of [
    m=>{m.tools[1].y-=80},
    m=>{m.tools[0].width=70},
    m=>{[m.digits[0].value,m.digits[1].value]=[2,1]},
    m=>{m.digits[3].x+=10},
  ]) {
    const m=metrics('landscape');mutate(m);
    assert.throws(()=>assertGeometry(m,fixtures.landscape.viewport));
  }
});

test('offscreen, covered, overlapping and clipped captions are real failures',()=>{
  for(const mutate of [
    m=>{m.tools[0].hit=false},
    m=>{m.tools[3].x=315},
    m=>{m.digits[1].x=m.digits[0].x},
    m=>{m.tools[2].textOverflow=true},
    m=>{m.internalHeight+=30},
  ]) {
    const m=metrics('portrait');mutate(m);
    assert.throws(()=>assertGeometry(m,fixtures.portrait.viewport));
  }
});

test('disabled Undo retains geometry obligations while enabled Undo must receive the action',()=>{
  const m=metrics('portrait'),undo=m.tools[0];
  undo.enabled=false;undo.hit=false;
  assert.equal(assertGeometry(m,fixtures.portrait.viewport).composition,'under-board');
  undo.enabled=true;
  assert.throws(()=>assertGeometry(m,fixtures.portrait.viewport),/centre is covered: Undo/);
  undo.enabled=false;undo.width=20;
  assert.throws(()=>assertGeometry(m,fixtures.portrait.viewport),/target too small: Undo/);
  undo.width=60;undo.y=600;
  assert.throws(()=>assertGeometry(m,fixtures.portrait.viewport),/intact controls panel|offscreen/);
});

test('shrinking the product controls panel cannot waive the explicit spacious landscape target',()=>{
  const m=metrics('landscape');m.controls.width=318;m.controls.height=230;
  m.digits.forEach((key,i)=>{key.x=450+(i%3)*81;key.y=120+Math.floor(i/3)*56;key.width=75;key.height=50;key.font=20});
  m.tools.forEach((key,i)=>{key.x=450+i*81;key.y=288;key.width=75;key.height=50});
  assert.throws(()=>assertGeometry(m,fixtures.landscape.viewport),/92×72/);
});

test('hint proof requires a visible board frame and an uncovered explanation, while allowing board panning',()=>{
  const m=metrics('portrait');m.frame.height=180;m.hint={x:10,y:270,width:300,height:240};
  assert.equal(assertGeometry(m,fixtures.portrait.viewport,{hint:true}).composition,'hint');
  m.hint.y=200;assert.throws(()=>assertGeometry(m,fixtures.portrait.viewport,{hint:true}),/overlap/);
});

test('every agreed Duo base and collapsed-bar viewport is explicitly present',()=>{
  for(const [width,height] of [[382,580],[594,368],[669,867],[951,585],[385,571],[382,618],[594,406],[669,902],[951,620],[385,609]])
    assert.ok(viewports.some(v=>v.touch&&v.width===width&&v.height===height),`missing ${width}×${height}`);
  assert.equal(new Set(viewports.map(v=>v.id)).size,viewports.length);
});

test('shared-shell neighbor smoke detects Suguru beside-board controls and clipped neighbor boards',()=>{
  const fixture=metrics('portrait');
  const m={...fixture,viewport:{width:1440,height:900},documentWidth:1440,controls:[...fixture.digits,...fixture.tools]};
  assert.equal(assertNeighborGeometry(m,{underBoard:true}).under_board,true);
  const beside=structuredClone(m);beside.controls.forEach(key=>{key.x+=350;key.y-=200});
  assert.throws(()=>assertNeighborGeometry(beside,{underBoard:true}),/under its board/);
  const clipped=structuredClone(m);clipped.frame.width-=20;
  assert.throws(()=>assertNeighborGeometry(clipped),/clipped/);
});

test('neighbor desktop permits ordinary vertical scrolling while preserving primary target layout',()=>{
  const fixture=metrics('portrait');
  const m={...fixture,viewport:{width:1440,height:400},documentWidth:1440,controls:[...fixture.digits,...fixture.tools].map((target,i)=>({...target,label:i<9?String(i+1):target.name}))};
  assert.equal(assertNeighborGeometry(m,{underBoard:true}).scrolling_required,true);
  m.controls[0].x=-10;
  assert.throws(()=>assertNeighborGeometry(m),/horizontally clipped: 1/);
  m.controls[0].x=10;m.controls[0].width=20;
  assert.throws(()=>assertNeighborGeometry(m),/too small: 1/);
});

test('focus evidence distinguishes primary tools and settings while preserving keyboard cell identity',async()=>{
  const previous={document:globalThis.document,innerWidth:globalThis.innerWidth,innerHeight:globalThis.innerHeight};
  const node=(tagName,textContent,attributes={})=>({tagName,textContent,getAttribute:name=>attributes[name]??null});
  try {
    globalThis.document={documentElement:{scrollWidth:390},querySelectorAll:()=>[]};globalThis.innerWidth=390;globalThis.innerHeight=844;
    const page={evaluate:async fn=>fn()},identities=[];
    for(const focus of [node('BUTTON','Undo',{'data-action':'undo'}),node('BUTTON','Notes'),node('SUMMARY','Options'),node('SUMMARY','More actions'),node('BUTTON','',{'data-cell':'7'})]) {
      document.activeElement=focus;identities.push((await measure(page)).focus);
    }
    assert.deepEqual(identities,['BUTTON:undo','BUTTON:Notes','SUMMARY:Options','SUMMARY:More actions','7']);
  } finally {
    for(const [name,value] of Object.entries(previous))if(value===undefined)delete globalThis[name];else globalThis[name]=value;
  }
});
