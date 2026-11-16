#!/usr/bin/env node
import {constants} from 'node:fs';
import {access,chmod,lstat,mkdir,readFile,realpath,writeFile} from 'node:fs/promises';
import {createHash} from 'node:crypto';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {parseArgs} from 'node:util';
import {emptyOwnedRoot} from '../sudoku-continuation/configure.mjs';
import {overlayInstance} from '../sudoku-continuation/followup.mjs';
import {correctionFacts,correctionRoles,correctionProcess,goalStageRole,goalStageProcess,goalStageID,goalStageRoleID,names,processID,testEntry,testHelpers} from './roles.mjs';
import {ownerDocument,ownerGoalPointer} from './stage-goal.mjs';

const hash=data=>createHash('sha256').update(data).digest('hex');
const inside=(root,value)=>value===root||value.startsWith(root+path.sep);
const artifact=id=>typeof id==='string'&&/^[A-Za-z0-9_-]+\/[A-Za-z0-9_.-]+$/.test(id);

async function bounded(filename,limit) {
  const info=await lstat(filename);
  if(!info.isFile()||info.isSymbolicLink()||info.size>limit)throw new Error('bounded regular input required: '+filename);
  const bytes=await readFile(filename);
  if(bytes.length!==info.size||bytes.length>limit)throw new Error('input changed during read: '+filename);
  return bytes;
}

function absolute(value,label) {
  if(typeof value!=='string'||!path.isAbsolute(value)||value.includes('\0'))throw new Error(label+' requires an absolute path');
  return path.resolve(value);
}

function argument(args,flag) {
  const index=args.indexOf(flag);
  if(index<0||args.indexOf(flag,index+1)>=0||index+1>=args.length)throw new Error('accepted runtime lacks exactly one '+flag);
  return absolute(args[index+1],flag);
}

export function correctionRuntime(parent,{capture,root,goalStager=fileURLToPath(new URL('./stage-goal.mjs',import.meta.url))}) {
  const {checker}=correctionFacts(parent),command=checker.command,env=command.env;
  const current=Boolean(parent.roles[names.check]);
  const previousPreview=current?absolute(env.RUNNER_SUDOKU_PREVIEW_ROOT,'accepted preview'):
    argument(command.args,command.args.includes('--preview-root')?'--preview-root':'--preview');
  const runtimeWritePaths=(command.runtime_write_paths||[]).filter(value=>value!==previousPreview);
  if(runtimeWritePaths.length!==(command.runtime_write_paths||[]).length-1)throw new Error('accepted preview has no exact runtime write grant');
  const required=['PATH','GOCACHE','GOMODCACHE','OPENSSL_CONF','RUNNER_SUDOKU_FRONTEND_HELPER'];
  if(required.some(name=>!env[name]))throw new Error('accepted runtime lacks pinned tool/cache settings');
  const pin=(name,flag)=>current?absolute(env['RUNNER_SUDOKU_'+name],name):argument(command.args,flag);
  return {
    capture:absolute(capture,'capture'),goalStager:absolute(goalStager,'goal stager'),admission:absolute(current?env.RUNNER_SUDOKU_ADMISSION:command.args[0],'admission'),
    node:current?absolute(command.args[0],'node'):argument(command.args,'--node'),go:pin('GO','--go'),
    modules:pin('MODULES','--modules'),playwright:pin('PLAYWRIGHT','--playwright'),chromium:pin('CHROMIUM','--chromium'),webkit:pin('WEBKIT','--webkit'),
    frontendHelper:absolute(env.RUNNER_SUDOKU_FRONTEND_HELPER,'frontend helper'),path:env.PATH,goCache:absolute(env.GOCACHE,'GOCACHE'),
    goModuleCache:absolute(env.GOMODCACHE,'GOMODCACHE'),opensslConf:absolute(env.OPENSSL_CONF,'OPENSSL_CONF'),runtimeWritePaths,
    preview:path.join(root,'preview')
  };
}

export function validateCorrectionGoal(goal,parent) {
  if(goal.followup?.run_id!==parent.id||goal.followup.revision!==parent.revision||goal.workspace!==parent.repository||
      typeof goal.request_id!=='string'||!goal.request_id.trim()||!Array.isArray(goal.corrections)||!goal.corrections.length)
    throw new Error('nonempty correction goal and exact accepted parent binding are required');
  const ids=new Set();
  for(const item of goal.corrections) {
    if(typeof item?.id!=='string'||!item.id.trim()||item.id!==item.id.trim()||item.id.length>128||ids.has(item.id)||
        typeof item.requirement!=='string'||!item.requirement.trim())throw new Error('corrections require unique bounded IDs and nonempty requirements');
    ids.add(item.id);
  }
  const accepted=goal.accepted_candidate;
  if(!accepted||!artifact(accepted.check_artifact)||!artifact(accepted.handoff_artifact)||!/^[a-f0-9]{64}$/.test(accepted.check_sha256||'')||!/^[a-f0-9]{64}$/.test(accepted.source_sha256||''))
    throw new Error('goal lacks exact accepted source/check/handoff identities');
}

export function correctionRequest(goalBytes,parent,ownerGoalArtifact) {
  if(!artifact(ownerGoalArtifact))throw new Error('completed goal-stage artifact ID required');
  const request=correctionRequestTemplate(goalBytes,parent);
  request.artifacts.owner_goal=ownerGoalArtifact;
  return request;
}

function correctionRequestTemplate(goalBytes,parent) {
  const goal=JSON.parse(new TextDecoder('utf-8',{fatal:true}).decode(goalBytes));
  validateCorrectionGoal(goal,parent);
  const facts=correctionFacts(parent),accepted=goal.accepted_candidate;
  return {request_id:goal.request_id,project:parent.project_id,process:processID,followup:{run_id:parent.id,revision:parent.revision},
    inputs:{request:ownerGoalPointer(goalBytes)},artifacts:{patch:facts.patch,checks:facts.checks,prior:accepted.check_artifact,handoff:accepted.handoff_artifact}};
}

export async function configureCorrections(values) {
  const root=await emptyOwnedRoot(absolute(values.root,'--root'));
  const instanceFile=await realpath(absolute(values.instance,'--instance'));
  const inspectFile=await realpath(absolute(values['parent-inspect'],'--parent-inspect'));
  const goalFile=await realpath(absolute(values.goal,'--goal'));
  const capture=await realpath(absolute(values['capture-baseline'],'--capture-baseline'));
  const goalStager=await realpath(absolute(values['goal-stager']||fileURLToPath(new URL('./stage-goal.mjs',import.meta.url)),'--goal-stager'));
  const [instanceBytes,parentBytes,goalBytes,captureBytes,goalStagerBytes]=await Promise.all([
    bounded(instanceFile,16<<10),bounded(inspectFile,16<<20),bounded(goalFile,40<<10),bounded(capture,64<<10),bounded(goalStager,64<<10)
  ]);
  const instance=JSON.parse(instanceBytes),parent=JSON.parse(parentBytes),goal=JSON.parse(goalBytes);
  correctionFacts(parent);
  validateCorrectionGoal(goal,parent);
  ownerDocument(goalBytes);
  const workspace=await realpath(parent.repository);
  if(inside(workspace,root)||inside(root,workspace))throw new Error('staged configuration must not overlap the canonical product workspace');
  const runtime=correctionRuntime(parent,{capture,root,goalStager});
  for(const binary of [runtime.node,runtime.go,runtime.admission])await access(binary,constants.X_OK);
  const roles=[...correctionRoles(parent,runtime),goalStageRole(runtime)],process=correctionProcess(parent),stage=goalStageProcess(parent);
  let project,projectFile;
  for(const reference of instance.projects||[]) {
    const filename=path.resolve(path.dirname(instanceFile),reference),candidate=JSON.parse(await bounded(filename,16<<10));
    if(candidate.id===parent.project_id) {
      if(project)throw new Error('duplicate accepted project ID');
      project=candidate;projectFile=filename;
    }
  }
  if(!project||await realpath(path.resolve(path.dirname(projectFile),project.workspace))!==workspace)throw new Error('configured project and accepted parent workspace differ');
  if(project.processes?.[processID]||project.processes?.[goalStageID]||[...Object.values(names),goalStageRoleID].some(id=>project.roles?.[id]))throw new Error('correction route already exists; preserve it and rejoin its saved request');
  const config=path.join(root,'config'),overlayProject=path.join(config,'projects',parent.project_id+'.json');
  const preserved=structuredClone(project);
  preserved.workspace=workspace;
  preserved.processes=Object.fromEntries(Object.entries(project.processes).map(([id,file])=>[id,path.resolve(path.dirname(projectFile),file)]));
  preserved.roles=Object.fromEntries(Object.entries(project.roles).map(([id,role])=>[id,{...role,file:path.resolve(path.dirname(projectFile),role.file)}]));
  preserved.processes[processID]=path.join(config,'processes',processID+'.json');
  preserved.processes[goalStageID]=path.join(config,'processes',goalStageID+'.json');
  preserved.default_process=processID;
  for(const role of roles)preserved.roles[role.id]={file:path.join(config,'roles',role.id+'.json')};
  const prepared=overlayInstance(instance,instanceFile,projectFile,overlayProject);
  prepared.enforce_work_limits=false;
  prepared.storage.database.max_runs=0;
  const documents=[
    [path.join(config,'instance.json'),prepared],[overlayProject,preserved],
    [path.join(config,'processes',processID+'.json'),process],[path.join(config,'processes',goalStageID+'.json'),stage],...roles.map(role=>[path.join(config,'roles',role.id+'.json'),role])
  ];
  for(const [filename,value] of documents)if(Buffer.byteLength(JSON.stringify(value,null,2)+'\n')>16<<10)throw new Error('configuration exceeds loader16KiB bound: '+filename);
  for(const directory of [root,config,path.join(config,'projects'),path.join(config,'processes'),path.join(config,'roles'),path.join(root,'requests'),runtime.preview])
    await mkdir(directory,{recursive:true,mode:0o700});
  await chmod(root,0o700);
  const json=async(filename,value,limit=16<<10)=>{
    const bytes=JSON.stringify(value,null,2)+'\n';
    if(Buffer.byteLength(bytes)>limit)throw new Error('staged document exceeds bound: '+filename);
    await writeFile(filename,bytes,{mode:0o600,flag:'wx'});
  };
  for(const [filename,value] of documents)await json(filename,value);
  const requestFile=path.join(root,'requests','correction.template.json'),stageFile=path.join(root,'requests','stage-goal.json');
  await json(requestFile,correctionRequestTemplate(goalBytes,parent));
  await json(stageFile,{request_id:goal.request_id+'-stage-owner-goal',project:parent.project_id,process:goalStageID,
    inputs:{request:goalBytes.toString('utf8')}},256<<10);
  const manifest={schema:1,prepared_at:new Date().toISOString(),status:'staged_qualification_pending',submitted:false,
    instance:path.join(config,'instance.json'),project_file:overlayProject,process:processID,request:requestFile,request_ready:false,workspace,preview:runtime.preview,
    goal_stage:{process:goalStageID,request:stageFile,output:'goal',binding:'Supply completed stage output goal as main artifacts.owner_goal using correctionRequest; template is deliberately incomplete'},
    parent:{id:parent.id,revision:parent.revision,inspect:inspectFile,sha256:hash(parentBytes)},goal:{path:goalFile,sha256:hash(goalBytes),bytes:goalBytes.length},
    original_instance:{path:instanceFile,sha256:hash(instanceBytes)},baseline_exporter:{path:capture,sha256:hash(captureBytes)},goal_stager:{path:goalStager,sha256:hash(goalStagerBytes)},
    project_verification:{entry:testEntry,helpers:testHelpers,status:'not_authored; future Runner task owns product implementation'},
    policy:{enforce_work_limits:false,max_runs:0,qualification:'Optional retained-run guard and baseline/exporter/profile fixtures must pass before installation; byte safeguards unchanged'},
    authority:'Staging only; no live service, policy, product source, preview process or progress database changed. Old processes/roles remain referenced unchanged. Core validates linked intake; product tests are authored/executed inside that future run.'};
  await json(path.join(root,'runtime.json'),manifest,64<<10);
  return manifest;
}

if(process.argv[1]&&path.resolve(process.argv[1])===fileURLToPath(import.meta.url)) {
  try {
    const {values}=parseArgs({options:Object.fromEntries(['root','instance','parent-inspect','goal','capture-baseline','goal-stager'].map(name=>[name,{type:'string'}]))});
    process.stdout.write(JSON.stringify(await configureCorrections(values),null,2)+'\n');
  } catch(error) {process.stderr.write(error.message+'\n');process.exitCode=1;}
}
