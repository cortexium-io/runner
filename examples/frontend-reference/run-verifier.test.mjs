import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {execFile} from 'node:child_process';
import {mkdir,mkdtemp,readFile,rm,symlink,writeFile} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {promisify} from 'node:util';
import {test} from 'node:test';
import {gunzipSync,gzipSync} from 'node:zlib';
import {assembleChecks,runVerifier} from './run-verifier.mjs';

const exec=promisify(execFile),sha=bytes=>createHash('sha256').update(bytes).digest('hex');
const png='iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jV8kAAAAASUVORK5CYII=';
const passing=`import {writeFile} from 'node:fs/promises';import {parseArgs} from 'node:util';const {values}=parseArgs({options:Object.fromEntries(['source','scratch','patch','reference'].map(name=>[name,{type:'string'}]))});const scratch=values.scratch;await writeFile(scratch+'/report.json',JSON.stringify({fixture:'synthetic; no observed product proof'}));await writeFile(scratch+'/phone.png',Buffer.from('${png}','base64'));await writeFile(scratch+'/project-result.json',JSON.stringify({status:'passed',reason:'Synthetic contract fixture passed',summary:'Synthetic image/file transport only',files:['report.json','phone.png']}));`;

async function fixture(t,script=passing) {
  const root=await mkdtemp(path.join(os.tmpdir(),'runner-project-verifier-'));t.after(()=>rm(root,{recursive:true,force:true}));
  const source=path.join(root,'source'),scratch=path.join(root,'scratch');await mkdir(source);await mkdir(scratch);
  const patch=path.join(root,'patch'),reference=path.join(root,'reference');
  await writeFile(patch,'exact current candidate patch');
  const request='Exact owner task';await writeFile(reference,JSON.stringify({version:1,kind:'owner-brief',status:'ready',request,request_bytes:Buffer.byteLength(request),request_sha256:sha(request)}));
  const entry='verify.mjs';await writeFile(path.join(source,entry),script);
  return {root,source,scratch,patch,reference,entry};
}

async function component(archive,name) {return (await exec('/usr/bin/tar',['-xOf',archive,name],{encoding:'buffer',maxBuffer:16<<20})).stdout;}

async function producerMetadata(root,archive,change) {
  const manifest=JSON.parse(await component(archive,'launcher.json')),files={};change(manifest);
  files['launcher.json']=Buffer.from(JSON.stringify(manifest)+'\n');
  for(const name of Object.keys(manifest.files))files[name]=await component(archive,name);
  const directory=path.join(root,'rewritten-producer');await mkdir(directory);
  for(const [name,bytes] of Object.entries(files))await writeFile(path.join(directory,name),bytes);
  await exec('/usr/bin/tar',['--format=ustar','-czf',archive,'-C',directory,...Object.keys(files)]);
}

async function projectHandoff(archive,producer) {
  const manifest=JSON.parse(await component(archive,'launcher.json')),original=await component(producer,'launcher.json'),proof=JSON.parse(original);
  assert.equal(manifest.project.sha256,sha(await readFile(producer)));
  assert.deepEqual(await component(archive,manifest.project.launcher_component),original);
  assert.deepEqual(Object.keys(manifest.project.files).sort(),Object.keys(proof.files).sort());
  for(const [name,{component:retained,sha256,bytes}] of Object.entries(manifest.project.files)) {
    const original=await component(producer,name),copied=await component(archive,retained);
    assert.deepEqual(copied,original);assert.equal(sha256,sha(copied));assert.equal(bytes,copied.length);
    assert.match(retained,/^project-proof-\d+\.(json|png)$/);
  }
  for(const image of manifest.project.images)assert.deepEqual(image,{name:image.name,...manifest.project.files[image.name]});
  return manifest;
}

test('fixed child verifier retains exact identities, declared proof and strict domain',async t=>{
  const f=await fixture(t),domain=await runVerifier(f),archive=path.join(f.scratch,'project-evidence.tar.gz');
  assert.equal(domain.outcome,'ready');assert.deepEqual(Object.keys(domain).sort(),['outcome','reason']);
  const manifest=JSON.parse(await component(archive,'launcher.json'));
  assert.equal(manifest.candidate.patch_sha256,sha(await readFile(f.patch)));
  assert.equal(manifest.owner_reference.sha256,sha(await readFile(f.reference)));
  assert.equal(manifest.verifier.sha256,sha(await readFile(path.join(f.source,f.entry))));
  assert.equal(manifest.files['phone.png'].sha256,sha(await component(archive,'phone.png')));
  assert.equal(manifest.images_inspected,false);assert.match(manifest.purpose,/mandatory/);
});

test('syntax, process exit and invalid script contracts return repairable retained diagnostics',async t=>{
  for(const script of ['syntax error {{{','process.exit(7)','console.log("bounded diagnostic");throw Error("fixture failure")','process.exit(0)',passing.replace("['report.json','phone.png']","['../outside.json']")])await t.test(script.slice(0,30),async t=>{
    const f=await fixture(t,script),domain=await runVerifier(f);
    assert.equal(domain.outcome,'changes_requested');
    const manifest=JSON.parse(await component(path.join(f.scratch,'project-evidence.tar.gz'),'launcher.json'));
    assert.equal(manifest.status,'changes_requested');assert.ok(manifest.error);
  });
});

test('invalid reference blocks before executing code and linked proof files are rejected',async t=>{
  const f=await fixture(t);await writeFile(f.reference,'{}');
  assert.equal((await runVerifier(f)).outcome,'blocked');
  const manifest=JSON.parse(await component(path.join(f.scratch,'project-evidence.tar.gz'),'launcher.json'));
  assert.equal(manifest.execution,undefined);
  const linked=await fixture(t,passing.replace("await writeFile(scratch+'/phone.png',Buffer.from('"+png+"','base64'));",""));
  await writeFile(path.join(linked.root,'outside.png'),Buffer.from(png,'base64'));await symlink(path.join(linked.root,'outside.png'),path.join(linked.scratch,'phone.png'));
  assert.equal((await runVerifier(linked)).outcome,'changes_requested');
});

test('assembly preserves focused and exact project bytes and binds independent judgment',async t=>{
  const f=await fixture(t);await runVerifier(f);
  const artifact=async(id,name,bytes)=>{const dir=path.join(f.root,id);await mkdir(dir);const filename=path.join(dir,name);await writeFile(filename,bytes);return filename;};
  const project=await artifact('attempt_project','result',await readFile(path.join(f.scratch,'project-evidence.tar.gz')));
  const focusedBytes=Buffer.from('{ "schema": 1, "status": "passed", "executed": 3, "failed": 0 }\n');
  const focused=await artifact('attempt_focus','result',focusedBytes);
  const archive=JSON.parse(await component(project,'launcher.json'));
  const judgment={verdict:'approved',patch_sha256:sha(await readFile(f.patch)),owner_request_sha256:archive.owner_reference.request_sha256,verifier_sha256:archive.verifier.sha256,project_artifact_id:'attempt_project/result',project_sha256:sha(await readFile(project)),inspected_images:['phone.png'],findings:[],gaps:[]};
  const experience=await artifact('attempt_review','report',JSON.stringify(judgment));
  const scratch=path.join(f.root,'assembled');await mkdir(scratch);
  assert.equal((await assembleChecks({...f,scratch,focused,project,experience})).outcome,'ready');
  const output=path.join(scratch,'project-evidence.tar.gz');
  assert.deepEqual(await component(output,'focused.json'),focusedBytes);
  const manifest=await projectHandoff(output,project);
  assert.equal(manifest.project.artifact_id,'attempt_project/result');assert.equal(manifest.project.sha256,sha(await readFile(project)));
  assert.equal(manifest.experience.artifact_id,'attempt_review/report');
  const names=(await exec('/usr/bin/tar',['-tf',output])).stdout.trim().split('\n').sort();
  assert.deepEqual(names,['focused.json','launcher.json','project-launcher.json','project-proof-0.png','project-proof-1.json','project-review.json']);
  for(const kind of ['stale patch','stale archive','missing image','unresolved gap','rejected']) {
    const modified=structuredClone(judgment);
    if(kind==='stale patch')modified.patch_sha256='f'.repeat(64);
    if(kind==='stale archive')modified.project_sha256='f'.repeat(64);
    if(kind==='missing image')modified.inspected_images=[];
    if(kind==='unresolved gap')modified.gaps=['Missing goal observation'];
    if(kind==='rejected')modified.verdict='changes_requested';
    await writeFile(experience,JSON.stringify(modified));
    const next=path.join(f.root,kind);await mkdir(next);
    assert.equal((await assembleChecks({...f,scratch:next,focused,project,experience})).outcome,kind==='rejected'?'changes_requested':'blocked');
  }
});

test('failed focused and project evidence reaches repair without fabricating a later review',async t=>{
  for(const status of ['failed','blocked'])await t.test('focused '+status,async t=>{
    const f=await fixture(t),focused=path.join(f.root,'focused');
    const bytes=Buffer.from(JSON.stringify({status,reason:'Current focused fixture '+status}));await writeFile(focused,bytes);
    const domain=await assembleChecks({...f,focused,project:'/not-produced',experience:'/not-produced'});
    assert.equal(domain.outcome,status==='failed'?'changes_requested':'blocked');
    const archive=path.join(f.scratch,'project-evidence.tar.gz');
    assert.deepEqual(await component(archive,'focused.json'),bytes);
    assert.equal(JSON.parse(await component(archive,'launcher.json')).experience,undefined);
  });
  for(const script of ['syntax error {{{',passing.replace("status:'passed'","status:'failed'"),passing.replace("status:'passed'","status:'blocked'")])await t.test('project '+script.slice(0,20),async t=>{
    const f=await fixture(t,script),observed=await runVerifier(f),project=path.join(f.scratch,'project-evidence.tar.gz');
    const focused=path.join(f.root,'focused');await writeFile(focused,'{"status":"passed"}');
    const scratch=path.join(f.root,'assembled');await mkdir(scratch);
    assert.equal((await assembleChecks({...f,scratch,focused,project,experience:'/not-produced'})).outcome,observed.outcome);
    const archive=path.join(scratch,'project-evidence.tar.gz');
    assert.match((await component(archive,'project-diagnostic.json')).toString(),/execution/);
    assert.equal((await projectHandoff(archive,project)).experience,undefined);
  });
});

test('complete failure and passing reports and all current images survive reserved producer names',async t=>{
  for(const status of ['failed','passed'])await t.test(status,async t=>{
    const report={status,checks:Array.from({length:1202},(_,i)=>({name:'synthetic detailed assertion '+i,passed:i<1174,detail:'Exact retained finding '+i})),coverage:{physical:false,fixture:true}};
    const imageNames=['phone.png','phone-secondary.png','phone-options.png','desktop.png','desktop-secondary.png','desktop-options.png'];
    const reserved=['focused.json','project-review.json','project-diagnostic.json','project-launcher.json'];
    const names=['project-evidence.json',...imageNames,...reserved];
    const script=passing+`await writeFile(scratch+'/project-evidence.json',JSON.stringify(${JSON.stringify(report)}));for(const name of ${JSON.stringify(imageNames)})await writeFile(scratch+'/'+name,Buffer.concat([Buffer.from('${png}','base64'),Buffer.from(name)]));for(const name of ${JSON.stringify(reserved)})await writeFile(scratch+'/'+name,JSON.stringify({producer:name}));await writeFile(scratch+'/project-result.json',JSON.stringify({status:'${status}',reason:'Synthetic detailed report fixture',files:${JSON.stringify(names)}}));`;
    const f=await fixture(t,script);await runVerifier(f);
    const project=path.join(f.scratch,'project-evidence.tar.gz'),focused=path.join(f.root,'focused'),experience=path.join(f.root,'review');
    await producerMetadata(f.root,project,proof=>{proof.files['phone.png'].component='launcher.json';proof.files['project-evidence.json'].component='focused.json';});
    const focusedBytes=Buffer.from('{ "status": "passed", "producer": "focused check" }\n');await writeFile(focused,focusedBytes);
    const proof=JSON.parse(await component(project,'launcher.json'));
    await writeFile(experience,JSON.stringify({verdict:'approved',patch_sha256:sha(await readFile(f.patch)),owner_request_sha256:proof.owner_reference.request_sha256,verifier_sha256:proof.verifier.sha256,project_artifact_id:path.basename(f.scratch)+'/project-evidence.tar.gz',project_sha256:sha(await readFile(project)),inspected_images:imageNames,findings:[],gaps:[]}));
    const scratch=path.join(f.root,'assembled');await mkdir(scratch);
    assert.equal((await assembleChecks({...f,scratch,focused,project,experience})).outcome,status==='failed'?'changes_requested':'ready');
    const output=path.join(scratch,'project-evidence.tar.gz'),manifest=await projectHandoff(output,project);
    assert.deepEqual(JSON.parse(await component(output,manifest.project.files['project-evidence.json'].component)),report);
    assert.equal(manifest.project.images.length,6);assert.deepEqual(await component(output,'focused.json'),focusedBytes);
    if(status==='failed')assert.equal(manifest.experience,undefined);
    else assert.deepEqual(await component(output,'project-review.json'),await readFile(experience));
  });
});

test('assembly output collision preserves existing proof and never retries persistence as validation',async t=>{
  const f=await fixture(t),focused=path.join(f.root,'focused');await writeFile(focused,'{"status":"failed","reason":"fixture"}');
  const archive=path.join(f.scratch,'project-evidence.tar.gz');await writeFile(archive,'existing retained proof');
  await assert.rejects(assembleChecks({...f,focused}),error=>error.code==='EEXIST'&&path.basename(error.path)==='project-evidence.tar.gz');
  assert.equal(await readFile(archive,'utf8'),'existing retained proof');
});

test('near-limit producer proof decodes but aggregate entry overflow blocks without trimming',async t=>{
  const script=`import {writeFile} from 'node:fs/promises';import {parseArgs} from 'node:util';const {values}=parseArgs({options:Object.fromEntries(['source','scratch','patch','reference'].map(name=>[name,{type:'string'}]))});const files=Array.from({length:64},(_,i)=>'proof-'+i+'.json');let total=0;for(let i=0;i<files.length;i++){const size=i===63?(16<<20)-(128<<10)-total:259585;const bytes=Buffer.alloc(size,120);bytes[0]=34;bytes[size-1]=34;await writeFile(values.scratch+'/'+files[i],bytes);total+=size;}console.log('d'.repeat(8192));await writeFile(values.scratch+'/project-result.json',JSON.stringify({status:'passed',reason:'Near-limit synthetic metadata fixture',summary:'s'.repeat(4096),files,detail:'e'.repeat(57000)}));`;
  const f=await fixture(t,script);assert.equal((await runVerifier(f)).outcome,'ready');
  const project=path.join(f.scratch,'project-evidence.tar.gz'),bytes=await readFile(project),expanded=gunzipSync(bytes).length;
  assert.ok(expanded>16<<20,'fixture must cross the former decoder bound');
  assert.ok(expanded<=(16<<20)+(256<<10));
  const focused=path.join(f.root,'focused'),experience=path.join(f.root,'review');await writeFile(focused,'{"status":"passed"}');await writeFile(experience,'{}');
  const scratch=path.join(f.root,'assembled');await mkdir(scratch);
  assert.equal((await assembleChecks({...f,scratch,focused,project,experience})).outcome,'blocked');
  const manifest=JSON.parse(await component(path.join(scratch,'project-evidence.tar.gz'),'launcher.json'));
  assert.equal(manifest.project.sha256,sha(bytes));assert.match(manifest.error,/entry bound/);
  assert.equal(manifest.project.handoff,'unavailable');assert.equal(manifest.project.files,undefined);assert.equal(manifest.project.launcher_component,undefined);
  assert.deepEqual((await exec('/usr/bin/tar',['-tf',path.join(scratch,'project-evidence.tar.gz')])).stdout.trim().split('\n').sort(),['focused.json','launcher.json']);
});

test('aggregate expansion overflow blocks instead of dropping full project details',async t=>{
  const script=passing+`for(const name of ['large-a.json','large-b.json'])await writeFile(scratch+'/'+name,JSON.stringify('x'.repeat((15<<19)-2)));await writeFile(scratch+'/project-result.json',JSON.stringify({status:'failed',reason:'Synthetic expansion fixture',files:['large-a.json','large-b.json','phone.png']}));`;
  const f=await fixture(t,script);assert.equal((await runVerifier(f)).outcome,'changes_requested');
  const project=path.join(f.scratch,'project-evidence.tar.gz'),focused=path.join(f.root,'focused');await writeFile(focused,JSON.stringify({status:'passed',detail:'x'.repeat(2<<20)}));
  const scratch=path.join(f.root,'assembled');await mkdir(scratch);
  assert.equal((await assembleChecks({...f,scratch,focused,project,experience:'/not-produced'})).outcome,'blocked');
  const manifest=JSON.parse(await component(path.join(scratch,'project-evidence.tar.gz'),'launcher.json'));
  assert.match(manifest.error,/expansion bound/);assert.equal(manifest.project.handoff,'unavailable');assert.equal(manifest.project.files,undefined);
});

test('truncated, changed, expanded or stale producer proof cannot become a readable passing handoff',async t=>{
  for(const kind of ['truncated','changed proof','expanded','stale candidate','stale owner'])await t.test(kind,async t=>{
    const f=await fixture(t);await runVerifier(f);
    const project=path.join(f.scratch,'project-evidence.tar.gz'),bytes=await readFile(project),focused=path.join(f.root,'focused');await writeFile(focused,'{"status":"passed"}');
    if(kind==='truncated')await writeFile(project,bytes.subarray(0,bytes.length-16));
    if(kind==='changed proof') {
      const tar=gunzipSync(bytes),index=tar.indexOf(Buffer.from('synthetic; no observed product proof'));assert.ok(index>0);tar[index]=88;
      await writeFile(project,gzipSync(tar));
    }
    if(kind==='expanded')await writeFile(project,gzipSync(Buffer.alloc((16<<20)+(256<<10)+1)));
    if(kind==='stale candidate')await writeFile(f.patch,'different current patch');
    if(kind==='stale owner') {const owner=JSON.parse(await readFile(f.reference));owner.request='Different owner task';owner.request_bytes=Buffer.byteLength(owner.request);owner.request_sha256=sha(owner.request);await writeFile(f.reference,JSON.stringify(owner));}
    const scratch=path.join(f.root,'assembled');await mkdir(scratch);
    assert.equal((await assembleChecks({...f,scratch,focused,project,experience:'/not-produced'})).outcome,'blocked');
    const output=path.join(scratch,'project-evidence.tar.gz'),manifest=JSON.parse(await component(output,'launcher.json'));
    assert.ok(manifest.error);assert.equal(manifest.project,undefined);
    assert.deepEqual((await exec('/usr/bin/tar',['-tf',output])).stdout.trim().split('\n').sort(),['focused.json','launcher.json']);
  });
});

test('unencodable candidate metadata retains a repair diagnostic instead of a raw launcher failure',async t=>{
  const script=passing+`await writeFile(scratch+'/project-result.json','{"status":"passed","reason":"fixture","files":["report.json","phone.png"],"deep":'+'['.repeat(12000)+'0'+']'.repeat(12000)+'}');`;
  const f=await fixture(t,script);assert.equal((await runVerifier(f)).outcome,'changes_requested');
  const manifest=JSON.parse(await component(path.join(f.scratch,'project-evidence.tar.gz'),'launcher.json'));
  assert.ok(manifest.error);assert.equal(manifest.project_result,undefined);assert.ok(manifest.files['phone.png']);
});
