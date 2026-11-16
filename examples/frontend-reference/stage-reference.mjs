#!/usr/bin/env node
import {constants} from 'node:fs';
import {createHash} from 'node:crypto';
import {lstat,open,realpath,unlink} from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {parseArgs} from 'node:util';

export const maxBriefBytes=128<<10;
export const purpose='Owner-supplied scope and references only; not candidate acceptance, observed verification or release approval.';

function absolute(value) {
  if(typeof value!=='string'||!path.isAbsolute(value)||value.includes('\0'))throw new Error('fixed absolute goal and scratch paths are required');
  return path.resolve(value);
}

// The shared executor supplies its immutable goal file. Never enumerate a source
// directory, follow a reference URL or interpret the text as a command/path.
async function readBrief(filename) {
  const before=await lstat(filename);
  if(!before.isFile()||before.isSymbolicLink()||before.size<1||before.size>maxBriefBytes)throw new Error('owner brief must be a bounded regular text file');
  const file=await open(filename,constants.O_RDONLY|constants.O_NOFOLLOW);
  try {
    const opened=await file.stat(),buffer=Buffer.alloc(maxBriefBytes+1);
    if(!opened.isFile()||opened.dev!==before.dev||opened.ino!==before.ino)throw new Error('owner brief changed during reading');
    let size=0;
    while(size<buffer.length) {
      const {bytesRead}=await file.read(buffer,size,buffer.length-size,null);
      if(bytesRead===0)break;
      size+=bytesRead;
    }
    const after=await file.stat();
    if(size!==before.size||size>maxBriefBytes||after.size!==before.size||after.mtimeMs!==before.mtimeMs)throw new Error('owner brief changed during reading');
    const bytes=buffer.subarray(0,size),request=new TextDecoder('utf-8',{fatal:true,ignoreBOM:true}).decode(bytes);
    if(!request.trim()||request.includes('\0'))throw new Error('owner brief must contain nonempty UTF-8 text');
    return {request,request_bytes:bytes.length,request_sha256:createHash('sha256').update(bytes).digest('hex')};
  } finally {await file.close();}
}

export async function stageReference({goal,scratch}) {
  goal=absolute(goal);
  // The shared worker authenticates its short scratch alias. Resolve it once;
  // every output stays in the same managed directory enforced by the sandbox.
  scratch=await realpath(absolute(scratch));
  const directory=await lstat(scratch);
  if(!directory.isDirectory()||directory.isSymbolicLink())throw new Error('owned scratch must be an existing regular directory');
  const files=[];
  let complete=false;
  try {
    // Reserve both names before writing; never overwrite a retained result.
    for(const name of ['reference.json','domain.json']) {
      const filename=path.join(scratch,name),handle=await open(filename,'wx',0o600);
      files.push({filename,handle});
    }
    let reference,domain;
    try {
      reference={version:1,kind:'owner-brief',status:'ready',purpose,...await readBrief(goal)};
      domain={outcome:'ready',reason:'Exact owner brief staged as reference only; current candidate proof and protected gates remain required.'};
    } catch {
      reference={version:1,kind:'owner-brief',status:'blocked',purpose};
      domain={outcome:'blocked',reason:'Owner brief is missing, linked, changed, oversized or invalid text; no reference was accepted.'};
    }
    for(const [index,value] of [reference,domain].entries()) {
      await files[index].handle.writeFile(JSON.stringify(value,null,2)+'\n');
      await files[index].handle.sync();
    }
    complete=true;
    return domain;
  } finally {
    for(const {handle} of files)await handle.close();
    if(!complete)for(const {filename} of files)await unlink(filename);
  }
}

if(process.argv[1]&&path.resolve(process.argv[1])===fileURLToPath(import.meta.url)) {
  try {
    const {values}=parseArgs({options:{goal:{type:'string'},scratch:{type:'string'}}});
    const domain=await stageReference(values);
    process.stdout.write(domain.reason+'\n');
  } catch(error) {process.stderr.write(error.message+'\n');process.exitCode=1;}
}
