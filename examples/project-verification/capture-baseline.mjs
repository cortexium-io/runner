#!/usr/bin/env node
import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {execFile} from 'node:child_process';
import {promisify,parseArgs} from 'node:util';
import {open,writeFile,lstat,realpath,mkdtemp,mkdir,chmod,link,rm} from 'node:fs/promises';
import path from 'node:path';
import {pathToFileURL} from 'node:url';

const execute=promisify(execFile),sha=bytes=>createHash('sha256').update(bytes).digest('hex');
const archiveLimit=16<<20;
const ignoredDirectories=['node_modules','.git','.cache','.codex','.qualification','.vite','coverage','test-results','playwright-report'];
const ignoredPrefixes=['internal/app/http/frontend_dist','frontend/.genny/static-pages','frontend/dist'];
const environment={...process.env,GIT_CONFIG_GLOBAL:'/dev/null',GIT_CONFIG_NOSYSTEM:'1',GIT_OPTIONAL_LOCKS:'0',GIT_CONFIG_COUNT:'0',COPYFILE_DISABLE:'1'};
for(const key of Object.keys(environment))if(key.startsWith('GIT_')&&!['GIT_CONFIG_GLOBAL','GIT_CONFIG_NOSYSTEM','GIT_OPTIONAL_LOCKS','GIT_CONFIG_COUNT'].includes(key))delete environment[key];

function relative(name) {
  assert.ok(name&&!path.isAbsolute(name)&&!name.split('/').some(part=>!part||part==='.'||part==='..'),'source path is not an ordinary relative file');
  return name;
}

function hardExcluded(name) {
  return name==='.runner-source-handoff.json'||name.split('/').some(part=>ignoredDirectories.includes(part))||
    ignoredPrefixes.some(prefix=>name===prefix||name.startsWith(prefix+'/'))||name.endsWith('.log')||name.endsWith('.tsbuildinfo')||path.basename(name)==='.DS_Store';
}

function excluded(name,selection) {
  return hardExcluded(name)||!selection.include.includes(name)&&selection.exclude.some(prefix=>name===prefix||name.startsWith(prefix+'/'));
}

async function bounded(pathname,limit) {
  const file=await open(pathname,'r');
  try {
    const info=await file.stat();assert.ok(info.isFile()&&info.size<=limit,'baseline input must be a bounded regular file');
    const bytes=await file.readFile();assert.equal(bytes.length,info.size,'baseline input changed while reading');
    return bytes;
  } finally {await file.close()}
}

async function command(binary,args,source,signal,maxBuffer=archiveLimit) {
  return (await execute(binary,args,{cwd:source,env:environment,encoding:'buffer',maxBuffer,signal})).stdout;
}

async function git(source,args,signal) {
  return command('/usr/bin/git',['--no-replace-objects','-c','core.fsmonitor=false','-c','core.hooksPath=/dev/null',
    '--git-dir',path.join(source,'.git'),'--work-tree',source,...args],source,signal);
}

function names(bytes) {
  assert.equal(Buffer.from(bytes.toString('utf8')).compare(bytes),0,'source paths must have lossless UTF-8 names');
  return [...new Set(bytes.toString('utf8').split('\0').filter(Boolean).map(relative))].sort((a,b)=>Buffer.compare(Buffer.from(a),Buffer.from(b)));
}

async function binding(options) {
  const goal=await bounded(options.request,128<<10),handoff=await bounded(options['accepted-handoff'],64<<10);
  const accepted=JSON.parse(goal).accepted_candidate;
  assert.match(accepted?.source_sha256??'',/^[0-9a-f]{64}$/,'goal requires the exact accepted source SHA256');
  return {goal_sha256:sha(goal),accepted_source_sha256:accepted.source_sha256,
    accepted_check_sha256:sha(await bounded(options['accepted-check'],archiveLimit)),
    handoff_sha256:sha(handoff),admission_sha256:sha(await bounded(options.admission,128<<20))};
}

async function admission(options,directory,signal) {
  await mkdir(directory);
  await command(options.admission,['--mode','admit-followup','--source',options.source,'--scratch',directory,
    '--accepted-check',options['accepted-check'],'--accepted-handoff',options['accepted-handoff'],'--request',options.request],options.source,signal,1<<20);
  assert.equal(JSON.parse(await bounded(path.join(directory,'domain.json'),16<<10)).outcome,'ready','qualified source admission did not return ready');
  const original=await bounded(options['accepted-handoff'],64<<10),forwarded=await bounded(path.join(directory,'handoff.json'),64<<10);
  assert.ok(original.equals(forwarded),'source admission did not preserve its authenticated handoff');
  return JSON.parse(forwarded);
}

async function capture(source,base,selection,signal,staging) {
  const untracked=new Set(names(await git(source,['ls-files','--others','--exclude-standard','-z'],signal)));
  const files=names(await git(source,['ls-files','--cached','--others','--exclude-standard','-z'],signal)).filter(name=>!excluded(name,selection));
  const omit=[':(exclude,literal).runner-source-handoff.json',':(exclude,glob)**/.DS_Store',':(exclude,glob)**/*.log',':(exclude,glob)**/*.tsbuildinfo',
    ...ignoredDirectories.flatMap(name=>[`:(exclude,glob)**/${name}`,`:(exclude,glob)**/${name}/**`]),
    ...[...ignoredPrefixes,...selection.exclude].map(name=>':(exclude,literal)'+name)];
  const diff=['diff','--binary','--full-index','--no-ext-diff','--no-textconv','--no-color','--no-renames','--src-prefix=a/','--dst-prefix=b/',base,'--'];
  const ordinary=await git(source,[...diff,'.',...omit],signal);
  // Git excludes apply after all positive pathspecs. Capture exact imported
  // fixture exceptions separately, so excluded history is never reintroduced.
  const fixtures=selection.include.length?await git(source,[...diff,...selection.include.map(name=>':(literal)'+name)],signal):Buffer.alloc(0);
  const patch=Buffer.concat([ordinary,fixtures]);
  assert.ok(patch.length<=archiveLimit,'baseline delta exceeds its artifact byte bound');
  const inventory=[];let total=0;
  for(const name of files) {
    signal?.throwIfAborted();
    const pathname=path.join(source,name);
    let info;try{info=await lstat(pathname)}catch(error){if(error.code==='ENOENT'&&!untracked.has(name))continue;throw error}
    assert.ok(info.isFile(),'baseline source includes a nonregular file');
    total+=info.size;assert.ok(total<=128<<20&&inventory.length<16000,'baseline source exceeds the qualified source bound');
    const bytes=await bounded(pathname,16<<20),mode=info.mode&0o111?'100755':'100644';
    inventory.push({path:name,kind:untracked.has(name)?'untracked':'tracked',sha256:sha(bytes),bytes:bytes.length,mode});
    if(staging&&untracked.has(name)) {
      const target=path.join(staging,'untracked',name);await mkdir(path.dirname(target),{recursive:true});
      await writeFile(target,bytes,{flag:'wx',mode:mode==='100755'?0o755:0o644});
      await chmod(target,mode==='100755'?0o755:0o644);
    }
  }
  return {patch,files:inventory,identity:sha(JSON.stringify({patch:sha(patch),files:inventory}))};
}

export async function captureBaseline(options,{signal}={}) {
  for(const key of ['source','scratch','admission','accepted-check','accepted-handoff','request']) {
    assert.ok(path.isAbsolute(options[key]??'')&&path.normalize(options[key])===options[key],`--${key} requires an absolute canonical path`);
  }
  options={...options,source:await realpath(options.source),scratch:await realpath(options.scratch)};
  const inside=(parent,child)=>child===parent||child.startsWith(parent+path.sep);
  assert.ok(!inside(options.source,options.scratch)&&!inside(options.scratch,options.source),'baseline source and owned scratch must be separate');
  const archive=path.join(options.scratch,'baseline.tar.gz'),domain=path.join(options.scratch,'domain.json');
  for(const output of [archive,domain])await lstat(output).then(()=>{throw Error('baseline outputs already exist')},error=>{if(error.code!=='ENOENT')throw error});
  const work=await mkdtemp(path.join(options.scratch,'.baseline-'));let published=false,ready=false;
  try {
    const inputs=await binding(options),handoff=await admission(options,path.join(work,'admission-before'),signal),base=handoff.source?.base;
    assert.match(base??'',/^[0-9a-f]{40}$/,'admitted handoff lacks its exact Git base');
    const checkpointCommit=handoff.initial_commit,checkpointTree=handoff.materialized_tree;
    assert.match(checkpointCommit??'',/^[0-9a-f]{40}$/,'admitted handoff lacks its exact local checkpoint commit');
    assert.match(checkpointTree??'',/^[0-9a-f]{40}$/,'admitted handoff lacks its materialized checkpoint tree');
    assert.equal((await git(options.source,['rev-parse','--verify',checkpointCommit+'^{commit}'],signal)).toString().trim(),checkpointCommit,'baseline must bind the exact local checkpoint commit');
    assert.equal((await git(options.source,['rev-parse','--verify',checkpointCommit+'^{tree}'],signal)).toString().trim(),checkpointTree,'local checkpoint tree differs from the authenticated materialized tree');
    const selection={exclude:[...new Set((handoff.source.exclude??'').split(',').filter(Boolean).map(relative))].sort(),
      include:[...new Set((handoff.source.include??'').split(',').filter(Boolean).map(relative))].sort()};
    for(const name of selection.include)assert.ok(!hardExcluded(name)&&selection.exclude.some(prefix=>name.startsWith(prefix+'/')),'admitted fixture exception must be an exact ordinary file inside excluded history');
    const staging=path.join(work,'capture');await mkdir(path.join(staging,'untracked'),{recursive:true});
    const captured=await capture(options.source,checkpointCommit,selection,signal,staging);
    const manifest={schema:1,status:'passed',kind:'accepted-source-baseline',base,checkpoint_commit:checkpointCommit,checkpoint_tree:checkpointTree,...inputs,
      source_manifest_sha256:captured.identity,
      source_manifest_scope:'ordinary-source-projection; accepted_source_sha256 is authenticated separately by admit-followup',
      projection:{excluded_prefixes:selection.exclude,included_files:selection.include,restore:'checkout checkpoint_commit and verify checkpoint_tree; omit exclusions except exact included fixtures; apply source.patch; copy untracked/; base retains upstream lineage only'},
      exclusions:{directories:ignoredDirectories,prefixes:ignoredPrefixes,files:['.runner-source-handoff.json','.DS_Store'],suffixes:['.log','.tsbuildinfo']},
      patch:{path:'source.patch',base_commit:checkpointCommit,base_tree:checkpointTree,sha256:sha(captured.patch),bytes:captured.patch.length},untracked_directory:'untracked',files:captured.files};
    const encoded=Buffer.from(JSON.stringify(manifest,null,2)+'\n');
    await writeFile(path.join(staging,'manifest.json'),encoded,{flag:'wx',mode:0o600});
    await writeFile(path.join(staging,'source.patch'),captured.patch,{flag:'wx',mode:0o600});
    const pending=path.join(work,'baseline.tar.gz');
    await command('/usr/bin/tar',['-czf',pending,'-C',staging,'manifest.json','source.patch','untracked'],options.scratch,signal);
    await chmod(pending,0o600);const archiveBytes=await bounded(pending,archiveLimit);
    await admission(options,path.join(work,'admission-after'),signal);
    const after=await capture(options.source,checkpointCommit,selection,signal);
    assert.equal(after.identity,captured.identity,'source changed while its accepted baseline was captured');
    assert.deepEqual(await binding(options),inputs,'baseline admission inputs changed during capture');
    signal?.throwIfAborted();
    const result={outcome:'ready',reason:'Accepted source authenticated before and after read-only delta capture',
      artifact:'baseline.tar.gz',sha256:sha(archiveBytes),bytes:archiveBytes.length,manifest_sha256:sha(encoded),base,checkpoint_commit:checkpointCommit,checkpoint_tree:checkpointTree,...inputs,source_manifest_sha256:captured.identity};
    await link(pending,archive);published=true;
    const pendingDomain=path.join(work,'domain.json');await writeFile(pendingDomain,JSON.stringify({outcome:result.outcome,reason:result.reason})+'\n',{flag:'wx',mode:0o600});
    await link(pendingDomain,domain);ready=true;
    return result;
  } finally {
    try{await rm(work,{recursive:true,force:true})}
    catch(error){if(ready)await rm(domain,{force:true});if(published)await rm(archive,{force:true});throw error}
    if(published&&!ready)await rm(archive,{force:true});
  }
}

if(process.argv[1]&&import.meta.url===pathToFileURL(path.resolve(process.argv[1])).href) {
  const {values}=parseArgs({options:Object.fromEntries(['source','scratch','admission','accepted-check','accepted-handoff','request'].map(name=>[name,{type:'string'}]))});
  const controller=new AbortController(),abort=()=>controller.abort();
  process.once('SIGTERM',abort);process.once('SIGINT',abort);
  try{await captureBaseline(values,{signal:controller.signal})}
  catch(error){process.stderr.write('Accepted source baseline failed: '+String(error.message).slice(0,1200)+'\n');process.exitCode=1}
  finally{process.off('SIGTERM',abort);process.off('SIGINT',abort)}
}
