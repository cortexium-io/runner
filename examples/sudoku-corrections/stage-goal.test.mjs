import assert from 'node:assert/strict';
import {mkdtemp,readFile,readdir,rm,symlink,writeFile} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {test} from 'node:test';
import {goalDigest,maxGoalBytes,ownerDocument,ownerGoalPointer,stageGoal} from './stage-goal.mjs';

test('owner staging preserves exact UTF-8 bytes and emits only nonapproval summary and strict domain',async()=>{
  const root=await mkdtemp(path.join(os.tmpdir(),'runner-owner-goal-'));
  try {
    const bytes=Buffer.from(' {\n "request_id":"owner", "corrections":[{"id":"C13","requirement":"Read official links; keep å 😀 intact."}], "captures":["/owner/capture.png"], "acceptance":{"no_release":true}\n}\n');
    const input=root+'/input.json';await writeFile(input,bytes);
    const summary=await stageGoal({goal:input,scratch:root});
    assert.ok(bytes.equals(await readFile(root+'/owner-goal.json')));
    assert.equal(summary.sha256,goalDigest(bytes));assert.equal(summary.kind,'owner-document-only');assert.match(summary.approval,/none/);
    const domain=JSON.parse(await readFile(root+'/domain.json'));
    assert.deepEqual(Object.keys(domain).sort(),['outcome','reason']);assert.equal(domain.outcome,'ready');assert.ok(domain.reason.includes(summary.sha256));
    assert.ok(domain.reason.length<512);assert.match(domain.reason,/no source or acceptance approval/);
    const pointer=ownerGoalPointer(bytes);assert.ok(Buffer.byteLength(pointer)<512);assert.equal(JSON.parse(pointer).sha256,summary.sha256);
    assert.doesNotMatch(pointer,/official links|\/owner\/capture/);
    await assert.rejects(stageGoal({goal:input,scratch:root}),/already exists/);
    assert.ok(bytes.equals(await readFile(root+'/owner-goal.json')));assert.ok(!(await readdir(root)).includes('owner-goal.json.tmp'));
  } finally {await rm(root,{recursive:true,force:true});}
});

test('invalid, oversized and symlink owner documents never publish approval or an artifact',async()=>{
  for(const bytes of [Buffer.from([0xff]),Buffer.from('{}'),Buffer.alloc(maxGoalBytes+1,0x20),
    Buffer.from(JSON.stringify({request_id:'x',corrections:[{id:'a',requirement:'one'},{id:'a',requirement:'two'}]}))]) {
    const root=await mkdtemp(path.join(os.tmpdir(),'runner-owner-invalid-'));
    try {
      await writeFile(root+'/input.json',bytes);await assert.rejects(stageGoal({goal:root+'/input.json',scratch:root}));
      assert.deepEqual(await readdir(root),['input.json']);
    } finally {await rm(root,{recursive:true,force:true});}
  }
  const root=await mkdtemp(path.join(os.tmpdir(),'runner-owner-link-'));
  try {
    await writeFile(root+'/input.json',JSON.stringify({request_id:'x',corrections:[{id:'a',requirement:'one'}]}));
    await symlink(root+'/input.json',root+'/linked.json');
    await assert.rejects(stageGoal({goal:root+'/linked.json',scratch:root}),/regular file/);
    assert.equal(ownerDocument(await readFile(root+'/input.json')).corrections,1);
  } finally {await rm(root,{recursive:true,force:true});}
});

test('original owner document retains its exact complete bytes and digest',{skip:!process.env.RUNNER_SUDOKU_OWNER_GOAL},async()=>{
  const bytes=await readFile(process.env.RUNNER_SUDOKU_OWNER_GOAL);
  assert.equal(bytes.length,17294);assert.equal(goalDigest(bytes),'5d9a1e19d68c08b532aec0ce066e28c8592325364a779e0ffba2b41f0fdeaa6a');
  const root=await mkdtemp(path.join(os.tmpdir(),'runner-original-owner-goal-'));
  try {
    const summary=await stageGoal({goal:process.env.RUNNER_SUDOKU_OWNER_GOAL,scratch:root});
    assert.equal(summary.corrections,14);assert.ok(bytes.equals(await readFile(root+'/owner-goal.json')));
  } finally {await rm(root,{recursive:true,force:true});}
});
