import assert from 'node:assert/strict';
import {createHash} from 'node:crypto';
import {lstat,mkdir,mkdtemp,readFile,readdir,rm,symlink,writeFile} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {test} from 'node:test';
import {maxBriefBytes,purpose,stageReference} from './stage-reference.mjs';

async function fixture(t) {
  const root=await mkdtemp(path.join(os.tmpdir(),'runner-reference-'));
  t.after(()=>rm(root,{recursive:true,force:true}));
  const scratch=path.join(root,'scratch'),goal=path.join(root,'goal.txt');
  await mkdir(scratch);
  return {root,scratch,goal};
}

test('stages exact Unicode/BOM text and opaque URLs with a truthful digest and strict domain',async t=>{
  const f=await fixture(t),request='\uFEFFOwner’s brief\r\n\nUse https://example.invalid/reference and $(never execute). 🙂\n';
  await writeFile(f.goal,request);
  assert.equal((await stageReference(f)).outcome,'ready');
  const reference=JSON.parse(await readFile(path.join(f.scratch,'reference.json')));
  assert.deepEqual(reference,{version:1,kind:'owner-brief',status:'ready',purpose,request,request_bytes:Buffer.byteLength(request),request_sha256:createHash('sha256').update(request).digest('hex')});
  const domain=JSON.parse(await readFile(path.join(f.scratch,'domain.json')));
  assert.deepEqual(Object.keys(domain).sort(),['outcome','reason']);
  assert.match(domain.reason,/reference only/);
  assert.deepEqual((await readdir(f.scratch)).sort(),['domain.json','reference.json']);
  assert.equal((await lstat(path.join(f.scratch,'reference.json'))).mode&0o777,0o600);
});

test('invalid reference inputs preserve a blocked diagnostic without acceptance claims',async t=>{
  for(const kind of ['missing','linked','directory','oversized','invalid UTF-8','empty','NUL'])await t.test(kind,async t=>{
    const f=await fixture(t);
    if(kind==='linked') {
      await writeFile(path.join(f.root,'original'),'not followed');
      await symlink(path.join(f.root,'original'),f.goal);
    } else if(kind==='directory')await mkdir(f.goal);
    else if(kind!=='missing')await writeFile(f.goal,kind==='oversized'?Buffer.alloc(maxBriefBytes+1,97):kind==='invalid UTF-8'?Buffer.from([0xc3,0x28]):kind==='empty'?' \n':'owner\0brief');
    assert.equal((await stageReference(f)).outcome,'blocked');
    const reference=JSON.parse(await readFile(path.join(f.scratch,'reference.json')));
    assert.deepEqual(reference,{version:1,kind:'owner-brief',status:'blocked',purpose});
    assert.deepEqual(Object.keys(JSON.parse(await readFile(path.join(f.scratch,'domain.json')))).sort(),['outcome','reason']);
  });
});

test('collisions and invalid path ownership never replace prior evidence',async t=>{
  for(const name of ['reference.json','domain.json'])await t.test(name,async t=>{
    const f=await fixture(t);
    await writeFile(f.goal,'owner brief');
    await writeFile(path.join(f.scratch,name),'prior evidence');
    await assert.rejects(stageReference(f),{code:'EEXIST'});
    assert.equal(await readFile(path.join(f.scratch,name),'utf8'),'prior evidence');
    assert.deepEqual(await readdir(f.scratch),[name]);
  });
  const f=await fixture(t);
  await assert.rejects(stageReference({goal:'relative',scratch:f.scratch}),/absolute/);
  await writeFile(f.goal,'exact brief through authenticated scratch alias');
  await symlink(f.scratch,path.join(f.root,'scratch-link'));
  assert.equal((await stageReference({goal:f.goal,scratch:path.join(f.root,'scratch-link')})).outcome,'ready');
  assert.deepEqual((await readdir(f.scratch)).sort(),['domain.json','reference.json']);
});
