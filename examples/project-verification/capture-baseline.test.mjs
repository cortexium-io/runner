import {test} from 'node:test';
import assert from 'node:assert/strict';
import {execFile} from 'node:child_process';
import {promisify} from 'node:util';
import {createHash} from 'node:crypto';
import {mkdtemp,mkdir,writeFile,readFile,readdir,chmod,lstat,rm,access,realpath} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {captureBaseline} from './capture-baseline.mjs';

const exec=promisify(execFile),git=(root,...args)=>exec('/usr/bin/git',['-C',root,...args],{env:{...process.env,GIT_CONFIG_GLOBAL:'/dev/null',GIT_CONFIG_NOSYSTEM:'1',GIT_OPTIONAL_LOCKS:'0'}});
const sha=bytes=>createHash('sha256').update(bytes).digest('hex');
async function file(root,name,bytes,mode=0o644){const p=path.join(root,name);await mkdir(path.dirname(p),{recursive:true});await writeFile(p,bytes);await chmod(p,mode)}

// The qualified admit-followup suite owns authentication. This fixed protocol
// fixture proves this exporter delegates twice with the exact granted inputs,
// preserves its receipt, and cannot turn admission failure into readiness.
async function fixture() {
  const root=await realpath(await mkdtemp(path.join(os.tmpdir(),'runner-source-baseline-'))),source=path.join(root,'source'),scratch=path.join(root,'scratch');
  await mkdir(source);await mkdir(scratch);await git(source,'init','-q');await git(source,'config','user.name','Fixture');await git(source,'config','user.email','fixture@example.invalid');
  await file(source,'README.md','accepted before\n');await file(source,'deleted.txt','remove me\n');await file(source,'script.sh','#!/bin/sh\nexit 0\n');
  await file(source,'tracked.bin',Buffer.from([0,255,1,2]));
  await file(source,'docs/evidence/old','old historical payload');
  await file(source,'docs/evidence/fixture.json','{"consumed":"before"}\n');
  await file(source,'.gitignore','node_modules/\nfrontend/dist/\n');await git(source,'add','.');await git(source,'commit','-qm','fixture base');
  const checkpoint=(await git(source,'rev-parse','HEAD')).stdout.trim(),tree=(await git(source,'rev-parse','HEAD^{tree}')).stdout.trim(),base='f'.repeat(40);
  await file(source,'README.md','accepted after\n');await git(source,'rm','-q','deleted.txt');await chmod(path.join(source,'script.sh'),0o755);
  await file(source,'tracked.bin',Buffer.from([0,255,3,4,0]));
  await file(source,'docs/evidence/fixture.json','{"consumed":"accepted after"}\n');
  await file(source,'assets/new.bin',Buffer.from([0,255,10,8,0]));await file(source,'draft folder/note ü.md','ordinary untracked\n',0o755);
  await file(source,'node_modules/cache','must not retain dependency');await file(source,'frontend/dist/build','must not retain build');await file(source,'docs/evidence/old','must not retain evidence');
  await git(source,'add','-u');await git(source,'commit','-qm','accepted tracked changes after materialization');
  const handoff={phase:'ready',initial_commit:checkpoint,materialized_tree:tree,source:{base,exclude:'docs/evidence',include:'docs/evidence/fixture.json'}},handoffPath=path.join(root,'handoff.json');
  await writeFile(handoffPath,JSON.stringify(handoff));await writeFile(path.join(source,'.runner-source-handoff.json'),JSON.stringify(handoff));
  const prior=path.join(root,'prior-check'),request=path.join(root,'goal.json'),control=path.join(root,'control.json'),admission=path.join(root,'admit');
  await writeFile(prior,'opaque authenticated artifact fixture');await writeFile(request,JSON.stringify({accepted_candidate:{source_sha256:'a'.repeat(64),check_sha256:'b'.repeat(64)}}));
  await writeFile(control,JSON.stringify({calls:0,mode:'pass'}));
  await file(root,'admit',`#!${process.execPath}
import {readFile,writeFile} from 'node:fs/promises';
import assert from 'node:assert/strict';
const args=Object.fromEntries(Array.from({length:(process.argv.length-2)/2},(_,i)=>[process.argv[2+i*2],process.argv[3+i*2]]));
assert.equal(args['--mode'],'admit-followup');
for(const [key,value] of Object.entries(${JSON.stringify({'--source':source,'--accepted-check':prior,'--accepted-handoff':handoffPath,'--request':request})}))assert.equal(args[key],value);
const control=${JSON.stringify(control)},state=JSON.parse(await readFile(control,'utf8'));state.calls++;await writeFile(control,JSON.stringify(state));
if(state.mode==='fail'||state.mode==='fail-second'&&state.calls===2)process.exit(2);
if(state.mode==='mutate-source'&&state.calls===2)await writeFile(${JSON.stringify(path.join(source,'README.md'))},'concurrent source change');
if(state.mode==='mutate-input'&&state.calls===2)await writeFile(${JSON.stringify(request)},JSON.stringify({accepted_candidate:{source_sha256:'c'.repeat(64)}}));
if(state.mode==='mutate-handoff'&&state.calls===2)await writeFile(args['--accepted-handoff'],(await readFile(args['--accepted-handoff'],'utf8'))+' ');
if(state.mode==='mutate-admission'&&state.calls===2)await writeFile(process.argv[1],(await readFile(process.argv[1],'utf8'))+'// changed after admission');
await writeFile(args['--scratch']+'/handoff.json',await readFile(args['--accepted-handoff']));
await writeFile(args['--scratch']+'/domain.json',JSON.stringify({outcome:state.mode==='not-ready'?'failed':'ready'}));
`,0o755);
  return {root,source,scratch,base,checkpoint,tree,control,options:{source,scratch,admission,'accepted-check':prior,'accepted-handoff':handoffPath,request}};
}

test('accepted baseline reconstructs from the authenticated local checkpoint when upstream is absent and HEAD has advanced',async()=>{
  const f=await fixture();
  try {
    const before=await git(f.source,'status','--porcelain=v1','--untracked-files=all');
    await assert.rejects(git(f.source,'cat-file','-e',f.base+'^{commit}'),'fixture upstream base must be absent');
    assert.notEqual((await git(f.source,'rev-parse','HEAD')).stdout.trim(),f.checkpoint);
    const result=await captureBaseline(f.options);
    assert.equal(result.outcome,'ready');assert.equal(result.base,f.base);
    assert.equal(result.checkpoint_commit,f.checkpoint);assert.equal(result.checkpoint_tree,f.tree);
    assert.deepEqual(await readdir(f.scratch),['baseline.tar.gz','domain.json']);
    const domain=JSON.parse(await readFile(path.join(f.scratch,'domain.json')));
    assert.deepEqual(Object.keys(domain).sort(),['outcome','reason'],'fixed-command wire result contains unknown fields');
    assert.equal(domain.outcome,'ready');
    assert.equal(JSON.parse(await readFile(f.control)).calls,2);
    assert.equal((await git(f.source,'status','--porcelain=v1','--untracked-files=all')).stdout,before.stdout,'capture mutated the source');
    const unpack=path.join(f.root,'unpack'),restored=path.join(f.root,'restored');await mkdir(unpack);
    await exec('/usr/bin/tar',['-xzf',path.join(f.scratch,'baseline.tar.gz'),'-C',unpack]);
    const manifest=JSON.parse(await readFile(path.join(unpack,'manifest.json')));
    assert.equal(manifest.status,'passed');
    const patch=await readFile(path.join(unpack,'source.patch'));
    assert.equal(manifest.patch.sha256,sha(patch));assert.equal(manifest.patch.bytes,patch.length);
    assert.ok(patch.includes(Buffer.from('GIT binary patch')),'tracked binary delta is missing');
    assert.ok(!patch.includes(Buffer.from('must not retain evidence')),'historical evidence leaked into the patch');
    assert.ok(patch.includes(Buffer.from('accepted after')),'changed consumed fixture was excluded from the delta');
    assert.equal(result.sha256,sha(await readFile(path.join(f.scratch,'baseline.tar.gz'))));
    assert.equal(result.manifest_sha256,sha(await readFile(path.join(unpack,'manifest.json'))));
    assert.equal(result.source_manifest_sha256,sha(JSON.stringify({patch:sha(patch),files:manifest.files})));
    for(const [field,input] of [['goal_sha256','request'],['handoff_sha256','accepted-handoff'],['accepted_check_sha256','accepted-check'],['admission_sha256','admission']])assert.equal(manifest[field],sha(await readFile(f.options[input])));
    assert.equal(manifest.accepted_source_sha256,'a'.repeat(64));assert.equal(manifest.base,f.base);
    assert.equal(manifest.checkpoint_commit,f.checkpoint);assert.equal(manifest.checkpoint_tree,f.tree);
    assert.equal(manifest.patch.base_commit,f.checkpoint);assert.equal(manifest.patch.base_tree,f.tree);
    assert.match(manifest.projection.restore,/checkout checkpoint_commit/);
    assert.equal(manifest.files.filter(row=>row.kind==='untracked').length,2);
    assert.ok(!manifest.files.some(row=>/node_modules|frontend\/dist|runner-source-handoff/.test(row.path)));
    assert.deepEqual(manifest.files.filter(row=>row.path.startsWith('docs/evidence/')).map(row=>row.path),['docs/evidence/fixture.json']);
    assert.deepEqual(manifest.projection.excluded_prefixes,['docs/evidence']);
    assert.deepEqual(manifest.projection.included_files,['docs/evidence/fixture.json']);
    assert.match(manifest.source_manifest_scope,/authenticated separately/);
    const listing=(await exec('/usr/bin/tar',['-tzf',path.join(f.scratch,'baseline.tar.gz')])).stdout;
    assert.equal(listing.split('\n').filter(name=>name==='untracked/assets/new.bin').length,1,'untracked payload was duplicated');
    await exec('/usr/bin/git',['clone','--quiet','--no-hardlinks',f.source,restored]);await git(restored,'checkout','-q',manifest.checkpoint_commit);
    assert.equal((await git(restored,'rev-parse','HEAD^{tree}')).stdout.trim(),manifest.checkpoint_tree);
    // Restore the admitted projection, not the old excluded history still
    // reachable in the exact base's Git objects. Its imported fixture stays.
    await rm(path.join(restored,manifest.projection.excluded_prefixes[0],'old'));
    await git(restored,'apply','--binary',path.join(unpack,'source.patch'));
    for(const row of manifest.files) {
      if(row.kind==='untracked')await file(restored,row.path,await readFile(path.join(unpack,'untracked',row.path)),row.mode==='100755'?0o755:0o644);
      assert.deepEqual(await readFile(path.join(restored,row.path)),await readFile(path.join(f.source,row.path)),row.path);
      const actual=await readFile(path.join(restored,row.path));assert.equal(row.sha256,sha(actual));assert.equal(row.bytes,actual.length);
      assert.equal(!!((await lstat(path.join(restored,row.path))).mode&0o111),row.mode==='100755',row.path+' executable mode');
    }
    await assert.rejects(access(path.join(restored,'deleted.txt')),error=>error.code==='ENOENT');
    await assert.rejects(access(path.join(restored,'docs/evidence/old')),error=>error.code==='ENOENT');
    assert.equal(await readFile(path.join(restored,'docs/evidence/fixture.json'),'utf8'),'{"consumed":"accepted after"}\n');
    assert.deepEqual(await readFile(path.join(restored,'assets/new.bin')),Buffer.from([0,255,10,8,0]));
    await assert.rejects(captureBaseline(f.options),/outputs already exist/);
    assert.equal(result.sha256,sha(await readFile(path.join(f.scratch,'baseline.tar.gz'))),'a repeat overwrote the canonical baseline');
  } finally {await rm(f.root,{recursive:true,force:true})}
});

test('failed admission or source/input mutation cannot publish ready or retain partial capture',async()=>{
  for(const mode of ['fail','fail-second','not-ready','mutate-source','mutate-input','mutate-handoff','mutate-admission']) {
    const f=await fixture();
    try {
      await writeFile(f.control,JSON.stringify({mode,calls:0}));
      await assert.rejects(captureBaseline(f.options),error=>mode==='mutate-source'?/source changed/.test(error.message):
        ['mutate-input','mutate-handoff','mutate-admission'].includes(mode)?/inputs changed/.test(error.message):mode==='not-ready'?/did not return ready/.test(error.message):error.code===2);
      assert.equal(JSON.parse(await readFile(f.control)).calls,['fail','not-ready'].includes(mode)?1:2,mode+' did not reach its intended boundary');
      assert.deepEqual(await readdir(f.scratch),[],mode+' retained partial output');
    } finally {await rm(f.root,{recursive:true,force:true})}
  }
});

test('missing or mismatched authenticated checkpoints reject without falling back to existing upstream or HEAD',async()=>{
  for(const mismatch of ['missing commit','different tree']) {
    const f=await fixture();
    try {
      const handoff=JSON.parse(await readFile(f.options['accepted-handoff']));
      handoff.source.base=f.checkpoint; // A valid upstream alternative must never be selected.
      if(mismatch==='missing commit')handoff.initial_commit='e'.repeat(40);
      else handoff.materialized_tree='d'.repeat(40);
      await writeFile(f.options['accepted-handoff'],JSON.stringify(handoff));
      await writeFile(path.join(f.source,'.runner-source-handoff.json'),JSON.stringify(handoff));
      await assert.rejects(captureBaseline(f.options),error=>mismatch==='missing commit'?error.code===128:/checkpoint tree differs/.test(error.message));
      assert.equal(JSON.parse(await readFile(f.control)).calls,1,'invalid checkpoint progressed beyond initial admission');
      assert.deepEqual(await readdir(f.scratch),[],'invalid checkpoint published an artifact or ready result');
    } finally {await rm(f.root,{recursive:true,force:true})}
  }
});
