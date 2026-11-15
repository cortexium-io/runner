import assert from 'node:assert/strict';

// Exact request-specific viewport/input contracts. Bar-height variants are
// separate real browser resizes, not labels inferred from a device user agent.
export const viewports = [
  {id:'phone-320',width:320,height:568,touch:true},
  {id:'phone-360',width:360,height:640,touch:true},
  {id:'phone-390',width:390,height:844,touch:true},
  {id:'phone-landscape',width:844,height:390,touch:true},
  {id:'tablet-portrait',width:768,height:1024,touch:true},
  {id:'tablet-landscape',width:1024,height:768,touch:true},
  {id:'desktop',width:1440,height:900,touch:false},
  {id:'desktop-short',width:1440,height:580,touch:false},
  {id:'desktop-resized',width:900,height:580,touch:false},
  {id:'duo-outer',width:382,height:580,touch:true},
  {id:'duo-outer-landscape',width:594,height:368,touch:true},
  {id:'duo-inner',width:669,height:867,touch:true},
  {id:'duo-inner-landscape',width:951,height:585,touch:true},
  {id:'duo-split',width:385,height:571,touch:true},
  {id:'duo-outer-bars',width:382,height:618,touch:true},
  {id:'duo-outer-landscape-bars',width:594,height:406,touch:true},
  {id:'duo-inner-bars',width:669,height:902,touch:true},
  {id:'duo-inner-landscape-bars',width:951,height:620,touch:true},
  {id:'duo-split-bars',width:385,height:609,touch:true},
];

const right = r => r.x+r.width;
const bottom = r => r.y+r.height;
const valid = r => r && ['x','y','width','height'].every(k=>Number.isFinite(r[k])) && r.width>0 && r.height>0;
const overlaps = (a,b) => Math.min(right(a),right(b))-Math.max(a.x,b.x)>1 && Math.min(bottom(a),bottom(b))-Math.max(a.y,b.y)>1;
const equal = (a,b) => Math.abs(a-b)<=1.5;
const horizontal = keys => keys.every((key,i)=>equal(key.y,keys[0].y) && (!i || key.x>=right(keys[i-1])-1));

// Affected shared-shell smoke only. These are not complete neighbor-game gates.
export function assertNeighborGeometry(m,{underBoard=false}={}) {
  assert.ok(valid(m.board)&&valid(m.frame),'neighbor board/frame is absent');
  assert.ok(m.documentWidth<=m.viewport.width+1,'neighbor page overflows horizontally');
  assert.ok(m.board.x>=m.frame.x-1&&right(m.board)<=right(m.frame)+1&&m.board.y>=m.frame.y-1&&bottom(m.board)<=bottom(m.frame)+1,'neighbor board is clipped by its shared frame');
  assert.ok(m.board.x>=-1&&right(m.board)<=m.viewport.width+1&&m.board.y>=-1&&bottom(m.board)<=m.viewport.height+1,'small neighbor board is outside the fresh desktop viewport');
  assert.ok(m.internalWidth<=m.clientWidth+1&&m.internalHeight<=m.clientHeight+1,'neighbor board has internal overflow');
  assert.ok(m.controls.length>0,'neighbor primary controls are absent');
  for(const control of m.controls) {
    assert.ok(valid(control)&&control.width>=24&&control.height>=24,'neighbor primary target is too small');
    assert.ok(control.x>=-1&&right(control)<=m.viewport.width+1&&control.y>=-1&&bottom(control)<=m.viewport.height+1,'neighbor control is outside the fresh desktop viewport');
    assert.ok(!overlaps(control,m.board),'neighbor primary controls overlap the board');
    if(underBoard)assert.ok(control.y>=bottom(m.board)-1,'Suguru desktop controls must remain under its board');
  }
  for(let i=0;i<m.controls.length;i++)for(let j=i+1;j<m.controls.length;j++)assert.ok(!overlaps(m.controls[i],m.controls[j]),'neighbor primary control footprints overlap');
  return {board:m.board,frame:m.frame,controls:m.controls.length,under_board:underBoard};
}

export function assertGeometry(m, viewport, {hint=false}={}) {
  assert.equal(m.viewport.width,viewport.width,'actual browser width differs from requested viewport');
  assert.equal(m.viewport.height,viewport.height,'actual browser height differs from requested viewport');
  assert.ok(m.documentWidth<=viewport.width+1,'page has horizontal overflow');
  assert.ok(valid(m.board)&&valid(m.frame),'actual board/frame is absent');
  assert.ok(m.board.x>=-1&&right(m.board)<=viewport.width+1,'board overflows viewport horizontally');
  assert.ok(m.board.x>=m.frame.x-1&&right(m.board)<=right(m.frame)+1,'board is clipped inside its frame');
  if(!hint) {
    assert.ok(m.board.y>=m.frame.y-1&&bottom(m.board)<=bottom(m.frame)+1,'board is vertically clipped inside its frame');
    assert.ok(m.internalWidth<=m.clientWidth+1&&m.internalHeight<=m.clientHeight+1,'board has internal clipping/overflow');
  }
  if(viewport.touch&&!hint) {
    assert.ok(m.board.y>=-1&&bottom(m.board)<=viewport.height+1,'touch board is outside the usable viewport');
  }
  if(hint) {
    assert.ok(m.frame.y>=-1&&bottom(m.frame)<=viewport.height+1&&m.frame.height>=80,'hint leaves no reachable visible board frame');
    assert.ok(valid(m.hint)&&m.hint.y>=-1&&bottom(m.hint)<=viewport.height+1,'requested hint explanation is clipped/offscreen');
    assert.ok(!overlaps(m.frame,m.hint),'hint explanation overlaps the board frame');
    return {composition:'hint',board:m.board,hint:m.hint};
  }
  assert.equal(m.digits.length,9,'exactly nine primary digit keys are required');
  assert.deepEqual(m.digits.map(k=>k.value),[1,2,3,4,5,6,7,8,9],'digit order must remain 1–9');
  assert.deepEqual(m.tools.map(k=>k.name),['Undo','Erase','Notes','Hint'],'four primary tools must remain Undo/Erase/Notes/Hint');
  assert.equal(m.permanentModeControls,0,'permanent pin/mode controls remain on the primary surface');
  const targets=[...m.digits,...m.tools];
  for(const target of targets) {
    assert.ok(valid(target)&&target.width>=24&&target.height>=24,`target too small: ${target.name??target.value}`);
    assert.ok(target.x>=-1&&right(target)<=viewport.width+1,`target is horizontally clipped: ${target.name??target.value}`);
    assert.ok(valid(m.controls)&&target.x>=m.controls.x-1&&right(target)<=right(m.controls)+1&&target.y>=m.controls.y-1&&bottom(target)<=bottom(m.controls)+1,`target escapes its intact controls panel: ${target.name??target.value}`);
    assert.ok(!target.textOverflow,`key/caption overflows its footprint: ${target.name??target.value}`);
    if(viewport.touch) {
      assert.ok(target.y>=-1&&bottom(target)<=viewport.height+1,`touch target is offscreen: ${target.name??target.value}`);
      assert.equal(target.hit,true,`target centre is covered: ${target.name??target.value}`);
    }
  }
  for(let i=0;i<targets.length;i++)for(let j=i+1;j<targets.length;j++)assert.ok(!overlaps(targets[i],targets[j]),'primary target footprints overlap');
  assert.ok(m.digits.every(k=>k.font>=18),'primary digits are not measurably readable');
  assert.ok(m.tools.every(k=>k.height>=40&&k.iconWidth>=16&&k.captionFont>=10),'primary tool icon/caption footprint is too small');
  assert.ok(horizontal(m.tools),'the four tools must form their own horizontal row');
  const side=viewport.touch&&viewport.width>viewport.height;
  if(side) {
    assert.ok(valid(m.controls)&&!overlaps(m.frame,m.controls),'touch landscape requires two intact non-overlapping panels');
    assert.ok(right(m.frame)<=m.controls.x+1 || right(m.controls)<=m.frame.x+1,'touch landscape controls must be beside the board');
    for(let row=0;row<3;row++)assert.ok(horizontal(m.digits.slice(row*3,row*3+3)),`digit row ${row+1} is not 3-wide`);
    for(let column=0;column<3;column++) {
      assert.ok(equal(m.digits[column].x,m.digits[column+3].x)&&equal(m.digits[column].x,m.digits[column+6].x),'the 3×3 digit columns are misaligned');
      assert.ok(bottom(m.digits[column])<=m.digits[column+3].y+1&&bottom(m.digits[column+3])<=m.digits[column+6].y+1,'the 3×3 digit rows overlap');
    }
    for(const tool of m.tools) {
      assert.ok(equal(tool.width,m.digits[0].width)&&equal(tool.height,m.digits[0].height),'landscape tools must match the number-key footprint');
    }
    // The candidate must not shrink its own panel to evade "space permits".
    // These explicitly requested wide landscape budgets fit the agreed target.
    if(viewport.width>=900&&viewport.height>=580)assert.ok(m.digits.every(k=>k.width>=91&&k.height>=71&&k.font>=29),'available large-pad space does not use the agreed 92×72/30px keys');
  } else {
    assert.ok(horizontal(m.digits),'portrait/tablet portrait/desktop digits must be one deliberate row');
    assert.ok(m.digits.every(k=>k.y>=bottom(m.board)-1),'portrait/desktop primary controls must sit under the board');
    if(!viewport.touch)assert.ok(!m.desktopPrimaryBeside,'desktop must not reuse the Duo primary composition');
  }
  assert.ok(m.tools.every(k=>k.y>=Math.max(...m.digits.map(bottom))-1),'tools must sit below the complete digit pad');
  return {
    composition:side?'touch-side-panels':'under-board',
    side:m.controls.x<m.frame.x?'left':'right',
    smallestTarget:Math.min(...targets.flatMap(k=>[k.width,k.height])),
    digitFont:Math.min(...m.digits.map(k=>k.font)),
    scrollingRequired:targets.some(k=>k.y<0||bottom(k)>viewport.height),
  };
}

// Read-only DOM evidence. No CSS mutation, localStorage writes or app internals.
export async function measure(page) {
  return page.evaluate(()=>{
    const visible=n=>!!n&&n.getClientRects().length>0&&getComputedStyle(n).visibility==='visible';
    const find=s=>[...document.querySelectorAll(s)].find(visible);
    const box=n=>{if(!n)return null;const r=n.getBoundingClientRect();return {x:r.x,y:r.y,width:r.width,height:r.height}};
    const board=find('.sudoku-play .puzzle-board'),frame=board?.closest('.player-board-frame');
    const controls=find('.sudoku-play .play-controls-panel')??find('.sudoku-play .primary-inputs');
    const record=n=>{
      const r=box(n),span=[...n.querySelectorAll('span')].find(s=>s.textContent.trim()),icon=n.querySelector('svg,img');
      const centre={x:r.x+r.width/2,y:r.y+r.height/2};
      const text=span??n,scale=text.offsetWidth?text.getBoundingClientRect().width/text.offsetWidth:1,font=parseFloat(getComputedStyle(text).fontSize)*scale;
      return {...r,font,hit:!!n.contains(document.elementFromPoint(centre.x,centre.y)),textOverflow:n.scrollWidth>n.clientWidth+1||n.scrollHeight>n.clientHeight+1,iconWidth:icon?.getBoundingClientRect().width??0,captionFont:font};
    };
    const digits=[...document.querySelectorAll('.sudoku-play .number-pad button')].filter(visible).map(n=>({...record(n),value:Number(n.textContent.trim())}));
    const tools=[...document.querySelectorAll('.sudoku-play .input-tools button')].filter(visible).map(n=>({...record(n),name:(n.textContent.trim().match(/Undo|Erase|Notes|Hint/)??[])[0]}));
    return {
      viewport:{width:innerWidth,height:innerHeight},documentWidth:document.documentElement.scrollWidth,
      board:box(board),frame:box(frame),controls:box(controls),digits,tools,
      internalWidth:board?.scrollWidth,internalHeight:board?.scrollHeight,clientWidth:board?.clientWidth,clientHeight:board?.clientHeight,
      hint:box(find('.sudoku-play .hint-panel')),
      permanentModeControls:[...document.querySelectorAll('.sudoku-play .play-controls-panel button')].filter(visible).filter(n=>/^(Pin digit|Cell first|Number first)$/.test(n.textContent.trim())).length,
      desktopPrimaryBeside:!!board&&digits.some(n=>n.y<box(board).y+box(board).height-1),
      selected:board?.querySelector('.selected')?.getAttribute('data-cell'),
      pinned:[...document.querySelectorAll('.sudoku-play .number-pad button')].filter(n=>n.getAttribute('aria-pressed')==='true').map(n=>Number(n.textContent.trim())),
      notesMode:find('.sudoku-play .input-tools button[aria-pressed]')?.getAttribute('aria-pressed'),
      focus:document.activeElement?.getAttribute('data-cell')??document.activeElement?.getAttribute('aria-label')??document.activeElement?.tagName,
    };
  });
}
