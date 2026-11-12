// Fixed first-flow observations. This never loads repository browser scripts.
import fs from 'node:fs/promises';
import path from 'node:path';
import crypto from 'node:crypto';
import {spawn} from 'node:child_process';
import {createRequire} from 'node:module';

const [runtime, chrome, executable, app, scratch, output] = process.argv.slice(2);
const require = createRequire(path.join(runtime, 'package.json'));
const {chromium} = require('playwright');
let server, browser, chromeProcess, origin;
let logBytes = 0, foreignRequests = 0;
let serverLogBytes=0;
const serverLogChunks=[];
let chromeLogBytes=0,chromeKeptBytes=0;
const chromeLogChunks=[];
let clickActions=0, lastCaseClicks=0;
const cases = [], images = [];
const serverPids=[],serverSettlements=new Set();
const root = path.join(scratch, 'local-instance');
await fs.mkdir(root, {recursive:true, mode:0o700});
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
const hash = b => crypto.createHash('sha256').update(b).digest('hex');

async function stopServer() {
  const owned=server;
  if(!owned)return;
  if(owned.exitCode!==null||owned.signalCode!==null){serverSettlements.add(owned.pid);return;}
  const stopped = new Promise(resolve => owned.once('exit', resolve));
  owned.kill('SIGTERM');
  const force = setTimeout(() => { if(owned.exitCode === null) owned.kill('SIGKILL'); }, 2000);
  try { await stopped; } finally { clearTimeout(force); }
  serverSettlements.add(owned.pid);
}
async function startServer() {
  const ready = path.join(root, 'ready.json');
  await fs.rm(ready, {force:true});
  server = spawn(executable, ['--listen','127.0.0.1:0','--data-dir',root,'--frontend-dir',path.join(app,'internal','app','http','frontend_dist'),'--ready-file',ready,'--fixture'], {
    cwd:app, detached:false, stdio:['ignore','pipe','pipe'],
    env:{PATH:process.env.PATH,HOME:root,TMPDIR:root,DB_DRIVER:'sqlite',DATABASE_URL:`file:${path.join(root,'fixture.db')}`},
  });
  server.on('error', () => {});
  serverPids.push(server.pid);
  for(const pipe of [server.stdout,server.stderr]) pipe.on('data', b => {
    logBytes += b.length;
    const kept=b.subarray(0,Math.max(0,65536-serverLogBytes-chromeKeptBytes));
    if(kept.length){serverLogChunks.push(kept);serverLogBytes+=kept.length;}
    if(logBytes+chromeLogBytes > 65536 && server.exitCode === null) server.kill('SIGTERM');
  });
  const end = Date.now()+15000;
  while(Date.now()<end) {
    if(server.exitCode !== null || logBytes>65536) throw Error('local app failed before bounded readiness');
    try {
      const b = await fs.readFile(ready);
      if(b.length>1024) throw Error('oversized startup identity');
      const r=JSON.parse(b), u=new URL(r.origin);
      if(r.pid!==server.pid || u.protocol!=='http:' || u.hostname!=='127.0.0.1' || !u.port || u.pathname!=='/' || u.search || u.username || u.password || u.hash) throw Error('foreign local startup identity');
      origin=u.origin; return;
    } catch(e) { if(e.code!=='ENOENT') throw e; }
    await delay(20); // Only bounded owned-process readiness, never a success witness.
  }
  throw Error('local app readiness absent');
}
async function startChrome() {
  const profile=path.join(root,'chrome-profile');
  await fs.mkdir(profile,{recursive:true,mode:0o700});
  chromeProcess=spawn(chrome,['--headless','--remote-debugging-port=0','--remote-debugging-address=127.0.0.1',`--user-data-dir=${profile}`,'--use-mock-keychain','--password-store=basic','--no-first-run','--no-default-browser-check','--disable-background-networking','--disable-component-update','--disable-sync','--disable-default-apps','about:blank'],{
    detached:false,stdio:['ignore','pipe','pipe'],env:{PATH:process.env.PATH,HOME:root,TMPDIR:root},
  });
  chromeProcess.on('error',()=>{});
  for(const pipe of [chromeProcess.stdout,chromeProcess.stderr])pipe.on('data',b=>{
    chromeLogBytes+=b.length;
    const kept=b.subarray(0,Math.max(0,65536-serverLogBytes-chromeKeptBytes));
    if(kept.length){chromeLogChunks.push(kept);chromeKeptBytes+=kept.length;}
    if(logBytes+chromeLogBytes>65536&&chromeProcess.exitCode===null)chromeProcess.kill('SIGTERM');
  });
  const end=Date.now()+15000;
  while(Date.now()<end) {
    if(chromeProcess.exitCode!==null) throw Error('owned headless Chrome exited before readiness');
    try {
      const port=Number((await fs.readFile(path.join(profile,'DevToolsActivePort'),'utf8')).split('\n')[0]);
      if(!Number.isInteger(port)||port<1||port>65535) throw Error('invalid owned Chrome port');
      browser=await chromium.connectOverCDP(`http://127.0.0.1:${port}`);return;
    } catch(e) { if(e.code!=='ENOENT') throw e; }
    await delay(20);
  }
  throw Error('owned headless Chrome readiness absent');
}
async function stopChrome() {
  if(!chromeProcess||chromeProcess.exitCode!==null||chromeProcess.signalCode!==null)return;
  const stopped=new Promise(resolve=>chromeProcess.once('exit',resolve));
  chromeProcess.kill('SIGTERM');
  const force=setTimeout(()=>chromeProcess.kill('SIGKILL'),2000);
  try{await stopped;}finally{clearTimeout(force);}
}
const signalCleanup=()=>{void browser?.close().catch(()=>{});void stopServer().catch(()=>{});void stopChrome().catch(()=>{});};
process.once('SIGTERM',signalCleanup);
process.once('SIGINT',signalCleanup);
const sel = (page,name) => page.locator(`[data-runner="${name}"]`);
async function visible(page,name,text) {
  const item=sel(page,name);
  await item.waitFor({state:'visible',timeout:10000});
  const actual=(await item.innerText()).trim();
  if(text!==undefined && !actual.includes(text)) throw Error(`${name}: required state absent`);
  return actual;
}
async function hit(page,name) {
  const item=sel(page,name);
  await item.scrollIntoViewIfNeeded();
  const box=await item.boundingBox();
  if(!box || !await item.isEnabled() || !await item.evaluate(el=>{
    const r=el.getBoundingClientRect(), x=r.left+r.width/2,y=r.top+r.height/2;
    const hit=document.elementFromPoint(x,y);
    return x>=0&&y>=0&&x<innerWidth&&y<innerHeight&&(hit===el||el.contains(hit));
  })) throw Error(`${name}: action is outside viewport, disabled or occluded`);
  await item.click();
  clickActions++;
}
async function capture(page,name) {
  if(images.some(im=>im.name===name)) throw Error('duplicate viewport');
  const b=await page.screenshot({fullPage:false,animations:'disabled'});
  if(b.length>8*1024*1024) throw Error('viewport exceeds8MiB');
  await fs.writeFile(path.join(output,name),b,{mode:0o600});
  images.push({name,sha256:hash(b),width:b.readUInt32BE(16),height:b.readUInt32BE(20)});
  await saveObservations();
}
async function saveObservations(){
  await fs.writeFile(path.join(output,'observations.json'),JSON.stringify({cases,images}),{mode:0o600});
}
async function record(name,witnesses) {
  const actions=clickActions-lastCaseClicks;lastCaseClicks=clickActions;
  cases.push({name,passed:true,actions,witnesses:witnesses.map(s=>String(s).slice(0,512))});
  await saveObservations();
}
async function context(user='alice',size={width:1440,height:900}) {
  const c=await browser.newContext({viewport:size,serviceWorkers:'block'});
  await c.route('**/*',route=> {
    const u=new URL(route.request().url());
    if(u.origin===origin) return route.continue();
    if(u.protocol!=='data:' && u.protocol!=='blob:') foreignRequests++;
    return route.abort('blockedbyclient');
  });
  await c.routeWebSocket('**/*',socket=>socket.close());
  const page=await c.newPage();
  await page.goto(origin,{waitUntil:'domcontentloaded'});
  await sel(page,'email').fill(`${user}@fixture.invalid`);
  await sel(page,'password').fill('Runner-fixture-only-12');
  await hit(page,'sign-in');
  await visible(page,'workspace');
  return {c,page};
}
async function setup(page,name) {
  await sel(page,'workspace').selectOption('shared-alpha');
  await hit(page,'new-flow');
  await sel(page,'flow-name').fill(name);
  await sel(page,'agent-version').selectOption('review-v1');
  await sel(page,'skill-version').selectOption('supplied-text-v1');
  await sel(page,'function-version').selectOption('report-v1');
  await visible(page,'typed-steps','text');
  await hit(page,'save-flow');
  await visible(page,'frozen-versions','review-v1');
}
async function run(page,text) {
  await sel(page,'supplied-text').fill(text);
  await hit(page,'run-flow');
  return visible(page,'run-id');
}
async function count(page,n) {
  const value=await visible(page,'model-invocations');
  if(value!==String(n)) throw Error(`model invocation count differs from${n}`);
  return value;
}
async function deniedAction(page,action) {
  const response=page.waitForResponse(r=>r.url().startsWith(origin+'/')&&r.status()===403);
  await action(); await response;
  return visible(page,'access-denied');
}

let primaryFailure;
try {
  await fs.mkdir(output,{recursive:true,mode:0o700});
  await startServer();
  await startChrome();
  const {page,c}=await context();
  await setup(page,'Runner representative flow');
  await capture(page,'chrome-desktop-setup-viewport.png');
  await page.setViewportSize({width:390,height:844});
  await visible(page,'frozen-versions','review-v1');
  await capture(page,'chrome-narrow-setup-viewport.png');
  await page.setViewportSize({width:1440,height:900});
  const first='First supplied text for the reusable review flow.';
  const id1=await run(page,first);
  await visible(page,'run-state','Awaiting acceptance');
  await visible(page,'retained-input',first);
  await visible(page,'current-step','Human');
  await visible(page,'responsible-party');
  await visible(page,'run-versions','report-v1');
  await count(page,1);
  await capture(page,'chrome-desktop-run-viewport.png');
  await hit(page,'accept-result');
  await visible(page,'run-state','Accepted');
  const result1=await visible(page,'saved-result');
  await hit(page,'run-again');
  const second='Second different supplied text for the same saved flow.';
  const id2=await run(page,second);
  if(id1===id2) throw Error('two inputs reused the same run identity');
  await visible(page,'run-state','Awaiting acceptance');
  await visible(page,'retained-input',second);
  const result2=await visible(page,'current-result');
  if(!result1 || result1===result2) throw Error('second input did not produce a distinct inspectable report');
  await record('two-inputs',[`separate retained runs ${id1} / ${id2}`,'same frozen Agent/Skill/function versions, distinct input/result'],18);

  const stale=await c.newPage();
  await stale.goto(page.url(),{waitUntil:'domcontentloaded'});
  const version1=await visible(stale,'result-version');
  await sel(page,'revision-instruction').fill('Make the report concise while preserving the supplied facts.');
  await hit(page,'request-revision');
  await visible(page,'run-state','Awaiting acceptance');
  const version2=await visible(page,'result-version');
  if(version1===version2) throw Error('revision did not create a new exact version');
  const conflict=stale.waitForResponse(r=>r.url().startsWith(origin+'/')&&r.status()===409);
  await hit(stale,'accept-result'); await conflict;
  await visible(stale,'version-conflict');
  await visible(page,'run-state','Awaiting acceptance');
  await capture(page,'chrome-desktop-revision-viewport.png');
  await hit(page,'accept-result');
  await visible(page,'accepted-version',version2);
  const saved=await visible(page,'saved-result');
  await page.reload();
  await visible(page,'saved-result',saved);
  await visible(page,'accepted-version',version2);
  await capture(page,'chrome-desktop-accepted-viewport.png');
  await page.setViewportSize({width:390,height:844});
  await visible(page,'saved-result',saved);
  await capture(page,'chrome-narrow-accepted-viewport.png');
  await page.setViewportSize({width:1440,height:900});
  await record('exact-acceptance',[`revision ${version1} -> ${version2}; stale acceptance HTTP409`, 'accepted exact version and saved result survive reload'],11);

  const resource=page.url();
  const {page:bob,c:bc}=await context('bob');
  await sel(bob,'workspace').selectOption('personal-bob');
  if(await sel(bob,'open-run').filter({hasText:id1}).count()||await sel(bob,'open-run').filter({hasText:id2}).count()||await sel(bob,'saved-result').count()) throw Error('foreign workspace report leaked through overview');
  const denial=bob.waitForResponse(r=>r.url().startsWith(origin+'/')&&r.status()===403);
  await bob.goto(resource,{waitUntil:'domcontentloaded'}); await denial;
  await visible(bob,'access-denied');
  await capture(bob,'chrome-desktop-tenant-denied-viewport.png');
  await record('tenant-session',['personal/shared workspace UI uses session-derived identity','foreign saved run/resource request HTTP403, no result exposed'],4);
  await bob.goto(origin,{waitUntil:'domcontentloaded'});
  await sel(bob,'workspace').selectOption('shared-alpha');
  await bob.goto(resource,{waitUntil:'domcontentloaded'});
  await visible(bob,'saved-result');
  await hit(page,'workspace-members');
  await hit(page,'remove-bob');
  await visible(page,'membership-state','Removed');
  await sel(bob,'supplied-text').fill('Removed member action must not dispatch');
  await deniedAction(bob,()=>hit(bob,'run-flow'));
  await deniedAction(bob,()=>bob.reload());
  await record('membership-removal',['shared member initially reads the retained resource','membership removed via owner UI; stale mutation and re-read HTTP403'],5);
  await bc.close();

  await page.goto(resource,{waitUntil:'domcontentloaded'});
  await hit(page,'worker-settings'); await hit(page,'revoke-worker-grant');
  await visible(page,'worker-grant-state','Revoked');
  await page.goto(resource,{waitUntil:'domcontentloaded'});
  await hit(page,'run-again'); await run(page,'Grant removed fixture');
  await visible(page,'run-state','Blocked'); await count(page,0);
  await record('worker-grant-removal',['grant revoked through trusted workspace UI','subsequent dispatch blocked, zero model invocations'],6);
  await hit(page,'worker-settings'); await hit(page,'grant-worker');
  await visible(page,'worker-grant-state','Granted');
  await setup(page,'Runner failure flow');
  await sel(page,'function-version').selectOption('fixture-failure-v1');
  await hit(page,'save-flow'); await run(page,'Formatter failure fixture');
  await visible(page,'run-state','Failed'); await visible(page,'failure-reason','formatter');
  if(await sel(page,'accept-result').isEnabled()) throw Error('failed formatter permits acceptance');
  await capture(page,'chrome-desktop-failure-viewport.png');
  await record('formatter-failure',['named deterministic formatter failure retained with reason','failed state cannot advance to acceptance'],6);

  await setup(page,'Runner unsupported capability');
  await sel(page,'worker').selectOption('fixture-unsupported');
  await hit(page,'save-flow'); await run(page,'Unsupported capability fixture');
  await visible(page,'run-state','Blocked'); await visible(page,'failure-reason','capability'); await count(page,0);
  await record('unsupported-worker',['unsupported enrolled Worker selected through UI','capability refusal before dispatch, zero model invocations'],6);

  await setup(page,'Runner receipt recovery');
  const receiptID=await run(page,'Receipt loss fixture');
  await visible(page,'run-state','Receipt pending'); await count(page,1);
  await hit(page,'recover-receipt'); await visible(page,'run-state','Awaiting acceptance');
  const receiptResult=await visible(page,'current-result');
  await stopServer(); await startServer();
  await page.goto(origin,{waitUntil:'domcontentloaded'});
  // Sessions are persisted by the normal App, not injected by this driver.
  await visible(page,'workspace'); await hit(page,'work-overview');
  await sel(page,'open-run').filter({hasText:receiptID}).click();
  await visible(page,'current-result',receiptResult); await count(page,1);
  await record('receipt-restart',[`retained ${receiptID} receipt rejoined across real process restart`,'one completed deterministic model invocation, identical report'],8);

  await setup(page,'Runner cancellation');
  await run(page,'Pending cancellation fixture');
  await visible(page,'run-state','Running'); await hit(page,'cancel-run');
  await visible(page,'run-state','Cancelled');
  await page.reload(); await visible(page,'run-state','Cancelled');
  await setup(page,'Runner uncertain effect');
  const uncertain=await run(page,'Uncertain effect fixture');
  await visible(page,'run-state','Uncertain'); await count(page,1);
  await page.reload(); await visible(page,'run-state','Uncertain'); await count(page,1);
  if(await sel(page,'retry-run').isEnabled()) throw Error('uncertain effect permits replacement invocation');
  await record('cancel-recovery',['cancelled work survives reload',`uncertain ${uncertain} retained without replacement; invocations1`],10);
  if(foreignRequests) throw Error('candidate attempted non-origin networking; actual dependency is unqualified');
  await fs.writeFile(path.join(output,'observations.json'),JSON.stringify({cases,images}),{mode:0o600});
} catch(error) {
  primaryFailure=error;
  throw error;
} finally {
  const results=await Promise.allSettled([(async()=>{try{if(browser)await browser.close();}finally{await stopChrome();}})(),stopServer()]);
  const failures=results.filter(r=>r.status==='rejected').map(r=>String(r.reason));
  const settled=process=>!process||process.exitCode!==null||process.signalCode!==null;
  if(!settled(server)||!settled(chromeProcess))failures.push('owned child settlement is unconfirmed');
  const cleanup={server_pids:serverPids,server_settled:serverPids.every(pid=>serverSettlements.has(pid)),chrome_pid:chromeProcess?.pid,chrome_settled:settled(chromeProcess),server_diagnostics:{output:Buffer.concat(serverLogChunks).toString('utf8'),bytes:logBytes,kept_bytes:serverLogBytes,truncated:logBytes>serverLogBytes},chrome_diagnostics:{output:Buffer.concat(chromeLogChunks).toString('utf8'),bytes:chromeLogBytes,kept_bytes:chromeKeptBytes,truncated:chromeLogBytes>chromeKeptBytes},failures};
  try{await fs.writeFile(path.join(output,'cleanup.json'),JSON.stringify(cleanup),{mode:0o600});}catch(error){failures.push('cleanup receipt: '+String(error));}
  if(failures.length) {
    if(primaryFailure)console.error('Owned cleanup failed:',failures.join('; '));
    else throw Error('Owned cleanup failed: '+failures.join('; '));
  }
}
