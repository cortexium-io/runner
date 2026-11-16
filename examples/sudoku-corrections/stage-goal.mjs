#!/usr/bin/env node
import {createHash} from 'node:crypto';
import {lstat,readFile,realpath,rename,rm,writeFile} from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {parseArgs} from 'node:util';

export const maxGoalBytes=40<<10;
export const goalDigest=bytes=>createHash('sha256').update(bytes).digest('hex');

// Preservation is deliberately separate from source admission and approval.
export function ownerDocument(bytes) {
  if(!bytes.length||bytes.length>maxGoalBytes)throw new Error('owner document exceeds its existing record bound');
  const text=new TextDecoder('utf-8',{fatal:true}).decode(bytes),goal=JSON.parse(text);
  if(!goal||typeof goal!=='object'||Array.isArray(goal)||typeof goal.request_id!=='string'||!goal.request_id.trim()||
      !Array.isArray(goal.corrections)||!goal.corrections.length)throw new Error('owner document needs a request ID and corrections');
  const ids=new Set();
  for(const item of goal.corrections) {
    if(typeof item?.id!=='string'||!item.id.trim()||item.id.length>128||ids.has(item.id)||
        typeof item.requirement!=='string'||!item.requirement.trim())throw new Error('owner corrections need unique IDs and requirements');
    ids.add(item.id);
  }
  return {kind:'owner-document-only',sha256:goalDigest(bytes),bytes:bytes.length,corrections:ids.size,
    approval:'none; preserving this document does not authenticate source or approve product acceptance'};
}

export function ownerGoalPointer(bytes) {
  const document=ownerDocument(bytes);
  return JSON.stringify({kind:'owner-goal-reference',sha256:document.sha256,bytes:document.bytes,
    purpose:'Read the unchanged owner_goal artifact for the full authoritative goal, all corrections, captures and obligations. Preserve progress and stop at verified preview for owner inspection.'});
}

export async function stageGoal({goal,scratch}) {
  if(!path.isAbsolute(goal||'')||!path.isAbsolute(scratch||''))throw new Error('absolute goal and owned scratch paths required');
  const info=await lstat(goal);
  if(!info.isFile()||info.isSymbolicLink()||info.size>maxGoalBytes)throw new Error('owner document must be a bounded regular file');
  const bytes=await readFile(goal);
  if(bytes.length!==info.size)throw new Error('owner document changed during read');
  const summary=ownerDocument(bytes),directory=await realpath(scratch);
  if(!(await lstat(directory)).isDirectory())throw new Error('owned scratch must be an existing directory');
  const temporary=path.join(directory,'owner-goal.json.tmp'),output=path.join(directory,'owner-goal.json');
  let created=false;
  try {
    await writeFile(temporary,bytes,{flag:'wx',mode:0o400});
    created=true;
    // Do not replace any earlier published evidence in a reused directory.
    try {await lstat(output);throw new Error('owner goal already exists');}catch(error) {if(error.code!=='ENOENT')throw error;}
    await rename(temporary,output);
    const reason=`owner-document-only sha256=${summary.sha256} bytes=${summary.bytes} corrections=${summary.corrections}; no source or acceptance approval`;
    await writeFile(path.join(directory,'domain.json'),JSON.stringify({outcome:'ready',reason})+'\n',{flag:'wx',mode:0o600});
    return summary;
  } finally {if(created)await rm(temporary,{force:true});}
}

if(process.argv[1]&&path.resolve(process.argv[1])===fileURLToPath(import.meta.url)) {
  try {
    const {values}=parseArgs({options:{goal:{type:'string'},scratch:{type:'string'}}});
    process.stdout.write(JSON.stringify(await stageGoal(values))+'\n');
  } catch(error) {process.stderr.write(String(error.message).slice(0,512)+'\n');process.exitCode=1;}
}
