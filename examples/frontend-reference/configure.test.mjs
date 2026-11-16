import assert from 'node:assert/strict';
import {readFile} from 'node:fs/promises';
import {test} from 'node:test';
import {ordinaryDelivery,referenceDefinitions,sourceReviewID,experienceReviewID,deliveryID,stageID} from './configure.mjs';
import {assemblyID,connectProjectVerification,ownerID,planID,projectCheckID,projectExperienceID,verifierEntry} from './project-verification.mjs';

test('new request review changes only future role prose and role references',async()=>{
  const json=async file=>JSON.parse(await readFile(new URL('../jacks-break/'+file,import.meta.url)));
  const process=await json('planned-frontend.json'),source=await json('roles/planned-frontend-review.json'),experience=await json('roles/experience-review.json');
  const before=structuredClone({process,source,experience}),next=ordinaryDelivery(process,source,experience);
  assert.deepEqual({process,source,experience},before);
  const restored=structuredClone(next.process);
  restored.steps.find(step=>step.id==='review').role=source.id;
  restored.steps.find(step=>step.id==='experience-review').role=experience.id;
  assert.deepEqual(restored,process); // Every checker, route, gate and decision is unchanged.
  for(const [old,role,id] of [[source,next.roles[0],sourceReviewID],[experience,next.roles[1],experienceReviewID]]) {
    assert.equal(role.id,id);
    const profile=structuredClone(role.execution);profile.instructions=old.execution.instructions;
    assert.deepEqual(profile,old.execution);
    assert.deepEqual(role.inputs,old.inputs);assert.deepEqual(role.outputs,old.outputs);
    assert.match(role.execution.instructions,/current|original/);
    assert.match(role.execution.instructions,/nine.family|nine puzzle families/);
    assert.match(role.execution.instructions,/native Safari/);
  }
  assert.match(next.roles[1].execution.instructions,/exact request text matching the original request/);
  assert.match(next.roles[1].execution.instructions,/Missing request-specific observations remain a blocker/);
  assert.equal(next.process.id,deliveryID);
});

test('stager uses one offline read-only fixed command and owns no model or broad write grant',()=>{
  const {role,process}=referenceDefinitions('/fixed/node','/fixed/stage-reference.mjs');
  assert.equal(role.execution.harness,'command');assert.equal(role.execution.mode,'shared');assert.equal(role.execution.access,'workspace-read');
  assert.deepEqual(role.execution.command,{args:['/fixed/node','/fixed/stage-reference.mjs','--goal','{goal}','--scratch','{scratch}'],artifact:'reference.json',result:'domain.json'});
  assert.equal(process.id,stageID);assert.deepEqual(process.inputs,{request:'text'});
  assert.deepEqual(process.outputs,{review_evidence:{from_step:'stage',output:'artifact'}});
  assert.equal(process.steps.length,1);assert.equal(process.max_attempts_per_step,1);assert.equal(process.attempt_timeout,'1m');
  assert.throws(()=>referenceDefinitions('relative','/fixed/script'),/absolute/);
});

test('connected verification keeps the focused checker and protected suffix while returning current diagnostics to the owner',async()=>{
  const json=async file=>JSON.parse(await readFile(new URL('../jacks-break/'+file,import.meta.url)));
  const process=await json('planned-frontend.json'),source=await json('roles/planned-frontend-review.json'),experience=await json('roles/experience-review.json');
  const planner=await json('roles/planned-plan.json'),owner=await json('roles/planned-frontend-owner.json');
  const ordinary=ordinaryDelivery(process,source,experience),before=structuredClone(ordinary);
  const runtime={node:'/fixed/node',launcher:'/fixed/run-verifier.mjs',nativeBinary:'/fixed/codex',path:'/fixed:/usr/bin:/bin',openssl:'/dev/null',modules:'/fixed/node_modules',goModules:'/fixed/go_modules',go:'/fixed/go',goCache:'/fixed/go-cache',playwright:'/fixed/playwright.mjs',chromium:'/fixed/chrome'};
  const next=connectProjectVerification(ordinary,planner,owner,runtime),step=id=>next.process.steps.find(item=>item.id===id),role=id=>next.roles.find(item=>item.id===id);
  assert.deepEqual(ordinary,before);
  assert.deepEqual(next.process.steps.slice(next.process.steps.findIndex(item=>item.id==='qualify')),ordinary.process.steps.slice(ordinary.process.steps.findIndex(item=>item.id==='qualify')));
  const original=structuredClone(process.steps.find(item=>item.id==='check')),focused=structuredClone(step('focused-check'));
  focused.id='check';focused.routes=original.routes;assert.deepEqual(focused,original);
  assert.equal(step('focused-check').routes.ready.next,'project-check');
  assert.equal(step('focused-check').routes.changes_requested.next,'check');
  assert.equal(step('project-check').routes.changes_requested.next,'check');
  assert.equal(step('measure').routes.ready.next,'project-experience');
  assert.equal(step('check').role,assemblyID);assert.equal(step('check').routes.changes_requested.next,'owner');
  assert.deepEqual(step('review').inputs.checks,{from_step:'check',output:'artifact'});
  assert.deepEqual(step('owner').inputs.checks,process.steps.find(item=>item.id==='owner').inputs.checks);
  assert.equal(step('plan').role,planID);assert.equal(step('owner').role,ownerID);
  for(const item of next.roles)assert.ok(Object.keys(item.inputs).length<=8);
  const check=role(projectCheckID).execution,view=role(projectExperienceID).execution;
  assert.equal(check.harness,'command');assert.equal(check.mode,'shared');assert.equal(check.access,'workspace-read');
  assert.equal(check.command.network,true);assert.ok(check.command.args.includes(verifierEntry));
  assert.equal(check.command.env.GOCACHE,runtime.goCache);assert.deepEqual(check.command.runtime_write_paths,[runtime.goCache]);
  assert.equal(view.harness,'native');assert.equal(view.access,'workspace-read');
  assert.deepEqual(view.native,{harness:'codex',binary:'/fixed/codex',context:'lean',tools:{mode:'native'},skills:{mode:'none'},network:false});
  for(const field of ['model','reasoning','max_input','max_output'])assert.equal(view[field],source.execution[field]);
  assert.match(role(experienceReviewID).execution.instructions,/do not claim you viewed them/);
});
