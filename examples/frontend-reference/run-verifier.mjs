#!/usr/bin/env node
import {constants} from 'node:fs';
import {createHash} from 'node:crypto';
import {lstat,mkdir,open,readFile,realpath,writeFile} from 'node:fs/promises';
import {spawn} from 'node:child_process';
import {gunzipSync} from 'node:zlib';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {parseArgs} from 'node:util';

const maxArtifact=16<<20,maxExpanded=maxArtifact+(256<<10),maxFile=8<<20,maxFiles=64;
const sha=bytes=>createHash('sha256').update(bytes).digest('hex');
const namePattern=/^[A-Za-z0-9][A-Za-z0-9_.-]{0,94}\.(json|png)$/;
const reason=value=>Buffer.from(String(value)).subarray(0,350).toString('utf8');
const absolute=value=>{if(typeof value!=='string'||!path.isAbsolute(value)||value.includes('\0'))throw new Error('configured absolute paths required');return path.resolve(value);};

async function regular(filename,limit,allowEmpty=false) {
  const before=await lstat(filename);
  if(!before.isFile()||before.isSymbolicLink()||before.size<(allowEmpty?0:1)||before.size>limit)throw new Error('bounded regular input required');
  const file=await open(filename,constants.O_RDONLY|constants.O_NOFOLLOW);
  try {
    const opened=await file.stat();
    if(opened.dev!==before.dev||opened.ino!==before.ino)throw new Error('input identity changed');
    const bytes=Buffer.alloc(before.size+1);let count=0;
    while(count<bytes.length) {const {bytesRead}=await file.read(bytes,count,bytes.length-count,null);if(!bytesRead)break;count+=bytesRead;}
    const after=await file.stat();
    if(count!==before.size||after.size!==before.size||after.mtimeMs!==before.mtimeMs)throw new Error('input changed during read');
    return bytes.subarray(0,count);
  } finally {await file.close();}
}

function child(binary,args,options={}) {
  return new Promise(resolve=>{
    const running=spawn(binary,args,options);let tail=Buffer.alloc(0),settled=false;
    const finish=value=>{if(!settled){settled=true;resolve({...value,diagnostic:tail.toString('utf8')});}};
    const collect=bytes=>{tail=Buffer.concat([tail,bytes]);if(tail.length>8192)tail=tail.subarray(tail.length-8192);};
    running.stdout.on('data',collect);running.stderr.on('data',collect);
    running.on('error',error=>finish({code:null,error:reason(error.message)}));
    running.on('close',(code,signal)=>finish({code,signal}));
  });
}

async function sourceEntry(source,entry) {
  if(typeof entry!=='string'||path.isAbsolute(entry)||entry.split('/').some(part=>!part||part==='.'||part==='..')||!entry.endsWith('.mjs'))throw new Error('configured relative verifier entry required');
  let filename=source;
  for(const part of entry.split('/')) {filename=path.join(filename,part);if((await lstat(filename)).isSymbolicLink())throw new Error('verifier entry must not traverse links');}
  return {filename,bytes:await regular(filename,128<<10)};
}

async function proofFiles(scratch,names) {
  if(!Array.isArray(names)||names.length>maxFiles||new Set(names).size!==names.length)throw new Error('bounded unique declared proof files required');
  const files={};let total=0;
  for(const name of names) {
    if(typeof name!=='string'||!namePattern.test(name)||['launcher.json','domain.json','project-result.json'].includes(name))throw new Error('proof paths must be flat JSON/PNG names');
    const bytes=await regular(path.join(scratch,name),maxFile);total+=bytes.length;
    if(total>maxArtifact-(128<<10))throw new Error('declared proof exceeds artifact bound');
    if(name.endsWith('.json'))JSON.parse(bytes);
    else if(bytes.length<24||!bytes.subarray(0,8).equals(Buffer.from([137,80,78,71,13,10,26,10]))||bytes.readUInt32BE(16)<1||bytes.readUInt32BE(16)>2000||bytes.readUInt32BE(20)<1||bytes.readUInt32BE(20)>2000)throw new Error('bounded PNG viewport required');
    files[name]=bytes;
  }
  return files;
}

function retainedFiles(manifest,files) {
  const encoded=Buffer.from(JSON.stringify(manifest)+'\n');
  if(encoded.length>128<<10)throw new Error('Candidate verifier metadata exceeds its retained bound.');
  const all={'launcher.json':encoded,...files};
  // Retained result/diagnostic metadata and USTAR headers/padding are bounded
  // separately from the compressed artifact. Reserve one tar blocking record.
  const expanded=1024+10240+Object.values(all).reduce((total,bytes)=>total+512+Math.ceil(bytes.length/512)*512,0);
  if(expanded>maxExpanded)throw new Error('retained project metadata exceeds its expansion bound');
  return all;
}

async function retain(scratch,manifest,files) {
  const all=retainedFiles(manifest,files);
  const packed=path.join(scratch,'retained-proof');await mkdir(packed,{mode:0o700});
  for(const [name,bytes] of Object.entries(all))await writeFile(path.join(packed,name),bytes,{flag:'wx',mode:0o600});
  const archive=path.join(scratch,'project-evidence.tar.gz'),handle=await open(archive,'wx',0o600);await handle.close();
  const result=await child('/usr/bin/tar',['--format=ustar','-czf',archive,'-C',packed,...Object.keys(all)],{env:process.env});
  if(result.code!==0||(await lstat(archive)).size>maxArtifact)throw new Error('bounded project evidence could not be retained');
}

// This boundary executes one configured candidate script, never a caller-selected
// command. Product assertions and browser/server cleanup belong to that script.
export async function runVerifier(values) {
  const source=await realpath(absolute(values.source)),scratch=await realpath(absolute(values.scratch));
  if(source===scratch||scratch.startsWith(source+path.sep))throw new Error('verifier scratch must be separate from source');
  const manifest={version:1,purpose:'Supplemental project verification only; mandatory built-image/native/release gates remain required.',status:'blocked',images_inspected:false,files:{}};
  let files={},outcome='blocked',message='Declared owner reference or current patch is invalid.';
  // Invalid declared authority is never converted into successful product proof.
  try {
    const patch=await regular(absolute(values.patch),maxArtifact,true),referenceBytes=await regular(absolute(values.reference),1<<20),reference=JSON.parse(referenceBytes);
    if(reference.version!==1||reference.kind!=='owner-brief'||reference.status!=='ready'||typeof reference.request!=='string'||!reference.request.trim()||reference.request_bytes!==Buffer.byteLength(reference.request)||reference.request_sha256!==sha(Buffer.from(reference.request)))throw new Error('owner reference identity differs');
    manifest.candidate={patch_sha256:sha(patch),source_binding:'Runner-authenticated current candidate in a read-only owned source workspace'};
    manifest.owner_reference={sha256:sha(referenceBytes),request_sha256:reference.request_sha256};
    manifest.runtime={node:process.version,binary:process.execPath};
    let selected;
    try {
      selected=await sourceEntry(source,values.entry);
      manifest.verifier={entry:values.entry,sha256:sha(selected.bytes)};
      const result=await child(process.execPath,[selected.filename,'--source',source,'--scratch',scratch,'--patch',absolute(values.patch),'--reference',absolute(values.reference)],{cwd:source,env:process.env});
      manifest.execution=result;
      if(result.code!==0)throw new Error('Candidate verifier exited unsuccessfully; inspect retained execution diagnostic.');
      const report=JSON.parse(await regular(path.join(scratch,'project-result.json'),64<<10));
      if(!['passed','failed','blocked'].includes(report.status)||typeof report.reason!=='string'||Buffer.byteLength(report.reason)>512)throw new Error('Candidate verifier returned an invalid result contract.');
      if(report.summary!==undefined&&(typeof report.summary!=='string'||Buffer.byteLength(report.summary)>4096))throw new Error('Candidate verifier summary exceeds its bound.');
      files=await proofFiles(scratch,report.files);
      manifest.project_result=report;
      outcome=report.status==='passed'?'ready':report.status==='failed'?'changes_requested':'blocked';
      message=report.reason||'Candidate verifier supplied no explanatory reason.';
    } catch(error) {
      outcome='changes_requested';message=reason(error.message);
      manifest.error=message;
      // Keep any declared, valid partial evidence, without trusting its status.
      try {const report=JSON.parse(await regular(path.join(scratch,'project-result.json'),64<<10));files=await proofFiles(scratch,report.files);}catch{}
    }
  } catch(error) {manifest.error=reason(error.message);}
  manifest.status=outcome;
  for(const [name,bytes] of Object.entries(files))manifest.files[name]={sha256:sha(bytes),bytes:bytes.length};
  try {retainedFiles(manifest,files);}catch(error) {
    // Invalid/overlarge candidate metadata is a verifier repair, not an I/O
    // failure or permission to publish a ready archive the decoder cannot read.
    delete manifest.project_result;outcome='changes_requested';message=reason(error.message);manifest.status=outcome;manifest.error=message;
  }
  await retain(scratch,manifest,files);
  const domain={outcome,reason:reason(message)};
  await writeFile(path.join(scratch,'domain.json'),JSON.stringify(domain)+'\n',{flag:'wx',mode:0o600});
  return domain;
}

function projectArchive(bytes) {
  const tar=gunzipSync(bytes,{maxOutputLength:maxExpanded}),files={};let offset=0,ended=false;
  while(offset+512<=tar.length) {
    const header=tar.subarray(offset,offset+512);offset+=512;
    if(header.every(byte=>byte===0)){ended=true;break;}
    const text=(start,length)=>header.subarray(start,start+length).toString('utf8').split('\0')[0];
    const name=text(0,100),sizeText=text(124,12).trim(),checksumText=text(148,8).trim();
    let checksum=0;for(let i=0;i<512;i++)checksum+=i>=148&&i<156?32:header[i];
    if(!namePattern.test(name)||files[name]||!['0','\0'].includes(String.fromCharCode(header[156]))||text(345,155)||!/^[0-7]+$/.test(sizeText)||!/^[0-7]+$/.test(checksumText)||parseInt(checksumText,8)!==checksum||Object.keys(files).length>maxFiles)throw new Error('Project archive ownership or entry differs.');
    const size=parseInt(sizeText,8);
    if(size<1||size>maxFile||offset+size>tar.length)throw new Error('Project archive entry exceeds its bound.');
    files[name]=tar.subarray(offset,offset+size);offset+=Math.ceil(size/512)*512;
  }
  if(!ended||tar.subarray(offset).some(byte=>byte!==0)||!files['launcher.json'])throw new Error('Project archive is incomplete.');
  const manifest=JSON.parse(files['launcher.json']);
  if(manifest.version!==1||!['ready','changes_requested','blocked'].includes(manifest.status)||!manifest.files||Object.keys(files).length!==Object.keys(manifest.files).length+1)throw new Error('Project verification result is invalid.');
  for(const [name,entry] of Object.entries(manifest.files))if(!files[name]||entry.sha256!==sha(files[name])||entry.bytes!==files[name].length)throw new Error('Project proof changed after collection.');
  return {manifest,images:Object.keys(manifest.files).filter(name=>name.endsWith('.png')).sort()};
}

function artifactIdentity(filename,bytes) {
  const id=path.basename(path.dirname(filename))+'/'+path.basename(filename);
  if(!/^[A-Za-z0-9_-]+\/[A-Za-z0-9_.-]+$/.test(id))throw new Error('Declared artifact identity is unavailable.');
  return {artifact_id:id,sha256:sha(bytes),bytes:bytes.length};
}

// Assemble only compact proof/judgment and original focused JSON. Images remain
// canonical in the original project artifact, never duplicated into this package.
export async function assembleChecks(values) {
  const scratch=await realpath(absolute(values.scratch));
  const files={};const manifest={version:1,kind:'frontend-checks',status:'blocked'};
  const finish=async(outcome,message)=>{
    manifest.status=outcome;await retain(scratch,manifest,files);
    const domain={outcome,reason:reason(message)};
    await writeFile(path.join(scratch,'domain.json'),JSON.stringify(domain)+'\n',{flag:'wx',mode:0o600});return domain;
  };
  const evaluate=async()=>{try {
    const patch=await regular(absolute(values.patch),maxArtifact,true),reference=await regular(absolute(values.reference),1<<20);
    const focused=await regular(absolute(values.focused),maxFile),owner=JSON.parse(reference),focus=JSON.parse(focused),patchSHA=sha(patch);
    if(owner.version!==1||owner.kind!=='owner-brief'||owner.status!=='ready'||typeof owner.request!=='string'||owner.request_sha256!==sha(Buffer.from(owner.request))||owner.request_bytes!==Buffer.byteLength(owner.request))throw new Error('Owner reference identity differs.');
    manifest.patch_sha256=patchSHA;manifest.owner_reference=artifactIdentity(values.reference,reference);
    manifest.focused={...artifactIdentity(values.focused,focused),patch_sha256:patchSHA,component:'focused.json'};files['focused.json']=focused;
    if(focus.status!=='passed') {
      if(!['failed','blocked'].includes(focus.status))throw new Error('Focused result is invalid.');
      return {outcome:focus.status==='failed'?'changes_requested':'blocked',message:focus.reason||'Current focused checks did not pass.'};
    }
    const project=await regular(absolute(values.project),maxArtifact),{manifest:proof,images}=projectArchive(project),projectIdentity=artifactIdentity(values.project,project);
    if(proof.candidate?.patch_sha256!==patchSHA||proof.owner_reference?.sha256!==sha(reference)||proof.owner_reference?.request_sha256!==owner.request_sha256)throw new Error('Project candidate or owner identity differs.');
    manifest.verifier=proof.verifier;
    manifest.project={...projectIdentity,patch_sha256:patchSHA,summary:{status:proof.status,reason:proof.project_result?.reason||proof.error||'',summary:proof.project_result?.summary||''},images:images.map(name=>({name,...proof.files[name]}))};
    if(proof.status!=='ready') {
      files['project-diagnostic.json']=Buffer.from(JSON.stringify({error:proof.error,execution:proof.execution,project_result:proof.project_result}));
      return {outcome:proof.status,message:proof.project_result?.reason||proof.error||'Current project verification did not pass.'};
    }
    const entry=await sourceEntry(await realpath(absolute(values.source)),values.entry),experience=await regular(absolute(values.experience),64<<10),judgment=JSON.parse(experience);
    if(proof.verifier?.entry!==values.entry||proof.verifier?.sha256!==sha(entry.bytes))throw new Error('Current verifier differs from the observed project proof.');
    if(judgment.patch_sha256!==patchSHA||judgment.owner_request_sha256!==owner.request_sha256||judgment.verifier_sha256!==sha(entry.bytes)||judgment.project_artifact_id!==projectIdentity.artifact_id||judgment.project_sha256!==projectIdentity.sha256||!Array.isArray(judgment.inspected_images)||!images.length||judgment.inspected_images.length!==images.length||new Set(judgment.inspected_images).size!==images.length||images.some(name=>!judgment.inspected_images.includes(name)))throw new Error('Independent image judgment is missing or belongs to different proof.');
    manifest.experience={...artifactIdentity(values.experience,experience),component:'project-review.json'};
    files['project-review.json']=experience;
    if(!['approved','changes_requested','blocked'].includes(judgment.verdict)||!Array.isArray(judgment.findings)||!Array.isArray(judgment.gaps)||judgment.verdict==='approved'&&judgment.gaps.length)throw new Error('Independent image verdict is invalid or retains an unresolved gap.');
    return {outcome:judgment.verdict==='approved'?'ready':judgment.verdict,message:judgment.verdict==='approved'?'Current focused, project and independent image proof assembled; mandatory qualification/native/release gates remain required.':'Independent project image review did not approve the current candidate.'};
  } catch(error) {manifest.error=reason(error.message);return {outcome:'blocked',message:'Current complete project proof is missing, stale or invalid.'};}};
  const result=await evaluate();
  return finish(result.outcome,result.message);
}

if(process.argv[1]&&path.resolve(process.argv[1])===fileURLToPath(import.meta.url)) {
  try {
    const {values}=parseArgs({options:{...Object.fromEntries(['source','scratch','patch','reference','entry','focused','project','experience'].map(name=>[name,{type:'string'}])),assemble:{type:'boolean'}}});
    const result=await (values.assemble?assembleChecks:runVerifier)(values);process.stdout.write(result.reason+'\n');
  } catch(error) {process.stderr.write(reason(error.message)+'\n');process.exitCode=1;}
}
