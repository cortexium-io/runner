import assert from 'node:assert/strict';
import {mkdir,mkdtemp,readFile,realpath,rm,writeFile} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {test} from 'node:test';
import {parentFixture,goalFixture} from '../../testdata/sudoku-corrections/configuration.mjs';
import {correctionRuntime,configureCorrections,validateCorrectionGoal} from './configure.mjs';
import {correctionFacts,correctionRoles,correctionProcess,names,processID,testEntry,testHelpers} from './roles.mjs';

test('correction route preserves native capabilities except explicit official-research network and separates ownership',()=>{
  const parent=parentFixture('/fixture',process.execPath),before=structuredClone(parent);
  const runtime=correctionRuntime(parent,{capture:'/fixed/capture-baseline.mjs',root:'/new/staged',goalSHA256:'c'.repeat(64)});
  const roles=correctionRoles(parent,runtime),byID=Object.fromEntries(roles.map(role=>[role.id,role]));
  assert.deepEqual(parent,before);assert.equal(roles.length,8);
  for(const [oldID,next] of [['sudoku-lead',names.lead],['sudoku-developer',names.developer],['sudoku-developer',names.author],['sudoku-source-review',names.source],['sudoku-ux-review',names.visual]]) {
    const profile=structuredClone(byID[next].execution);profile.instructions=parent.roles[oldID].execution.instructions;
    assert.deepEqual(profile,parent.roles[oldID].execution);assert.deepEqual(byID[next].outputs,{report:'text'});
  }
  const research=structuredClone(byID[names.research].execution);
  research.instructions=parent.roles['sudoku-source-review'].execution.instructions;delete research.native.network;
  assert.deepEqual(research,parent.roles['sudoku-source-review'].execution);
  assert.equal(byID[names.research].execution.native.network,true);
  const checker=byID[names.check].execution;
  assert.equal(checker.harness,'command');assert.equal(checker.mode,'shared');assert.equal(checker.access,'workspace-read');
  assert.deepEqual(checker.command.args,[process.execPath,'{workspace}/'+testEntry,'--source','{workspace}','--scratch','{scratch}','--goal','{goal}',
    '--baseline','{dependency.baseline.artifact}','--prior','{artifact.prior}','--checks','{artifact.checks}']);
  assert.equal(checker.command.result,'domain.json');assert.equal(checker.command.artifact,'evidence.tar.gz');
  assert.equal(checker.command.env.RUNNER_SUDOKU_PREVIEW_ROOT,'/new/staged/preview/'+'c'.repeat(64));
  assert.deepEqual(checker.command.runtime_write_paths,['/fixture/cache',runtime.preview]);
  assert.equal(byID[names.baseline].execution.command.artifact,'baseline.tar.gz');
  assert.equal(byID[names.baseline].execution.command.network,false);
  assert.match(byID[names.lead].execution.instructions,/manifest.status passed before admitting any writer/);
  assert.match(byID[names.lead].execution.instructions,/Current C13 requires official sharing research/);
  assert.match(byID[names.lead].execution.instructions,/BOTH writers depend_on:\["baseline","sharing-research"\]/);
  assert.match(byID[names.lead].execution.instructions,/omit irrelevant research/);
  assert.match(byID[names.lead].execution.instructions,/missing\/incomplete archive/);
  assert.match(byID[names.lead].execution.instructions,/never means acceptance/);
  assert.ok(byID[names.author].execution.instructions.includes(testHelpers));
  const author=byID[names.author].execution.instructions;
  for(const contract of ['domain.json contains EXACTLY outcome and reason','source_files exact sorted Go fileProof','<=88 entries/84 PNGs',
    'no directory headers, ./ prefixes, symlinks or duplicate names','RUNNER_SUDOKU_ADMISSION --mode admit-followup',
    'OMIT --request','archive bytes UNCHANGED','domain.reason records probe passed and archive SHA','emulated boundary evidence'])
    assert.ok(author.includes(contract),'missing fixed-command/source-admission contract: '+contract);
  assert.equal(checker.command.env.RUNNER_SUDOKU_ADMISSION,runtime.admission);
  for(const role of roles) {
    assert.ok(Buffer.byteLength(JSON.stringify(role,null,2))<16<<10,role.id+' exceeds loader bound');
    assert.doesNotMatch(role.execution.instructions||'',/\/SKILL\.md|146geometry|20journeys|A little hint|Show the next move/);
  }
  for(const kind of [names.source,names.visual])assert.match(byID[kind].execution.instructions,/every|Every/);
  const definition=correctionProcess(parent);
  assert.equal(definition.id,processID);assert.equal(definition.tasks.max_concurrent,parent.process.tasks.max_concurrent);
  assert.equal(definition.max_attempts_per_step,parent.process.max_attempts_per_step);
  assert.equal(definition.attempt_timeout,parent.process.attempt_timeout);
  assert.deepEqual(definition.tasks.roles,Object.values(names).filter(id=>id!==names.lead));
});

test('accepted correction parents retain pinned capabilities and admit a different nonempty correction set',()=>{
  const historical=parentFixture('/fixture',process.execPath);
  const firstRuntime=correctionRuntime(historical,{capture:'/fixed/capture-baseline.mjs',root:'/first',goalSHA256:'c'.repeat(64)});
  const parent={...structuredClone(historical),id:'run_new_correction_parent',revision:19,
    roles:Object.fromEntries(correctionRoles(historical,firstRuntime).map(role=>[role.id,role])),process:correctionProcess(historical)};
  parent.task_graph.root_run_id=parent.id;
  const before=structuredClone(parent);
  const nextRuntime=correctionRuntime(parent,{capture:firstRuntime.capture,root:'/next',goalSHA256:'d'.repeat(64)});
  assert.deepEqual({...nextRuntime,preview:firstRuntime.preview},firstRuntime);
  assert.deepEqual(correctionFacts(parent).nativeRoles,Object.fromEntries(['lead','developer','source','visual'].map(kind=>[kind,names[kind]])));
  const nextRoles=correctionRoles(parent,nextRuntime),nextProcess=correctionProcess(parent);
  assert.equal(nextRoles.length,8);assert.deepEqual(nextProcess,parent.process);
  for(const role of nextRoles.filter(role=>role.execution.harness==='native')) {
    const previous=parent.roles[role.id].execution;
    assert.deepEqual({...role.execution,instructions:previous.instructions},previous);
    assert.match(role.execution.instructions,/actual corrections\[\] IDs/);
    assert.doesNotMatch(role.execution.instructions,/every fourteen|all fourteen|every C01|C01–C14/);
  }
  const goal=goalFixture(parent);goal.corrections=[{id:'timer-copy',requirement:'Preserve current play while correcting the requested timer label.'}];
  assert.doesNotThrow(()=>validateCorrectionGoal(goal,parent));
  for(const corrections of [[],[goal.corrections[0],goal.corrections[0]],[{id:'',requirement:'required'}],[{id:'X',requirement:''}],[{id:'X'}]])
    assert.throws(()=>validateCorrectionGoal({...goal,corrections},parent),/correction|require/);
  const broken=structuredClone(parent);delete broken.roles[names.check].execution.command.env.RUNNER_SUDOKU_CHROMIUM;
  assert.throws(()=>correctionRuntime(broken,{capture:firstRuntime.capture,root:'/next',goalSHA256:'d'.repeat(64)}),/CHROMIUM requires an absolute path/);
  assert.deepEqual(parent,before);
});

test('staging preserves prior routes/source and records linked intake without dispatch',async()=>{
  const root=await realpath(await mkdtemp(path.join(os.tmpdir(),'runner-sudoku-correction-config-')));
  try {
    const parent=parentFixture(root,process.execPath),goal=goalFixture(parent);
    await mkdir(parent.repository);
    const oldProject={id:parent.project_id,name:'Existing project',workspace:parent.repository,default_process:'old',processes:{old:'old-process.json'},roles:{old:{file:'old-role.json'}}};
    const instance={projects:['project.json'],concurrency:1,max_active_attempts:2,task_concurrency:2,enforce_work_limits:false,
      reconcile_interval:'5m',storage:{max_managed:'8GiB',min_free:'5GiB',max_artifact:'16MiB',keep_runs:'168h',logs:{keep_for:'168h',max_per_attempt:'64KiB'},database:{max_record:'40KiB',max_size:'48MiB',max_runs:128}}};
    for(const [name,value] of [['parent.json',parent],['goal.json',goal],['project.json',oldProject],['instance.json',instance]])
      await writeFile(path.join(root,name),JSON.stringify(value),{mode:0o600});
    await writeFile(path.join(root,'capture.mjs'),'// synthetic source-only exporter; never executed\n');
    const options={root:root+'/staged',instance:root+'/instance.json','parent-inspect':root+'/parent.json',goal:root+'/goal.json','capture-baseline':root+'/capture.mjs'};
    const staged=await configureCorrections(options);
    assert.equal(staged.status,'staged_qualification_pending');assert.equal(staged.submitted,false);
    const prepared=JSON.parse(await readFile(staged.instance)),project=JSON.parse(await readFile(staged.project_file));
    const changed=structuredClone(prepared.storage);changed.database.max_runs=128;
    assert.deepEqual(changed,instance.storage);assert.equal(prepared.storage.database.max_runs,0);
    for(const key of ['concurrency','max_active_attempts','task_concurrency','enforce_work_limits'])assert.equal(prepared[key],instance[key]);
    assert.equal(project.default_process,processID);assert.equal(project.processes.old,root+'/old-process.json');assert.equal(project.roles.old.file,root+'/old-role.json');
    const request=JSON.parse(await readFile(staged.request));
    assert.deepEqual(request.followup,{run_id:parent.id,revision:parent.revision});
    assert.equal(request.inputs.request,JSON.stringify(goal));assert.equal(request.artifacts.prior,goal.accepted_candidate.check_artifact);
    assert.equal(staged.preview,path.join(root,'staged','preview',staged.goal.sha256));
    assert.deepEqual(JSON.parse(await readFile(root+'/project.json')),oldProject);
    assert.deepEqual(JSON.parse(await readFile(root+'/instance.json')),instance);
    assert.equal(await readFile(root+'/parent.json','utf8'),JSON.stringify(parent));
    await assert.rejects(configureCorrections(options),/non-empty root/);
  } finally {await rm(root,{recursive:true,force:true});}
});
