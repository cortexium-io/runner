// Fixed trusted operator bridge. No candidate-selected path, command or URL.
import fs from 'node:fs';
import {spawn} from 'node:child_process';
import {once} from 'node:events';
async function stream(readable){let total=0;for await(const chunk of readable){total+=chunk.length;if(total>16*1024*1024)throw Error('export exceeds16MiB');if(!process.stdout.write(chunk))await once(process.stdout,'drain');}}
const read=(file,limit)=>{try{const s=fs.lstatSync(file);if(!s.isFile()||s.isSymbolicLink()||s.size>limit)throw Error('owned control file differs');return JSON.parse(fs.readFileSync(file));}catch(e){if(e.code==='ENOENT')return null;throw e;}};
const [action,value,...extra]=process.argv.slice(2);
if(extra.length)throw Error('foreign control arguments');
const request=read('/work/control/request.json',1024);
if(request&&(request.version!==1||!Number.isSafeInteger(request.sequence)||request.sequence<1||request.sequence>4||!['start','stop'].includes(request.action)))throw Error('foreign lifecycle request');
if(action==='export'&&value===undefined){
 const file='/proof/evidence.tar.gz';
 if(fs.existsSync(file)){const s=fs.lstatSync(file);if(!s.isFile()||s.isSymbolicLink()||s.size>16*1024*1024)throw Error('foreign/oversized producer export');await stream(fs.createReadStream(file));}
 else{
  const root='/proof/viewports',image=/^chrome-(desktop|narrow)-(setup|run|revision|accepted|failure|tenant-denied|grant-denied|cancelled)-viewport\.png$/;
  const names=fs.readdirSync(root).filter(n=>image.test(n)||['observations.json','cleanup.json','runtime.json'].includes(n));
  let total=0;
  for(const name of names){const s=fs.lstatSync(root+'/'+name);if(!s.isFile()||s.isSymbolicLink()||s.size>(image.test(name)?8*1024*1024:128*1024))throw Error('foreign partial output');total+=s.size;}
  if(!names.length||names.length>163||total>16*1024*1024)throw Error('partial output absent/oversized');
  const child=spawn('/bin/tar',['--format=ustar','--blocking-factor=1','-czf','-','-C',root,...names],{env:{PATH:'/usr/bin:/bin'},stdio:['ignore','pipe','pipe']});
  const ended=once(child,'close');let diagnostic=0;
  child.stderr.on('data',data=>{diagnostic+=data.length;if(diagnostic>4096)child.kill('SIGTERM');});
  try{await stream(child.stdout);const [code]=await ended;if(code!==0||diagnostic>4096)throw Error('partial export failed');}finally{if(child.exitCode===null&&child.signalCode===null)child.kill('SIGTERM');}
 }
}else if(action==='status'&&value===undefined){
 let ready=null;const file='/app-state-anchor/ready.json';
 const body=read(file,1024);
 if(body){const stat=fs.statSync(file);if(body.origin!=='http://127.0.0.1:9044'||!Number.isSafeInteger(body.pid)||body.pid<1)throw Error('foreign ready identity');ready={...body,modified_ms:stat.mtimeMs};}
 process.stdout.write(JSON.stringify({request,ready,result:read('/work/result.json',128*1024)}));
}else if(action==='cancel'&&value===undefined){
 fs.writeFileSync('/work/cancel','cancel\n',{mode:0o600});
}else if(action==='reply'&&value?.length<=4096){
 const reply=JSON.parse(value);
 if(!request||reply.sequence!==request.sequence||reply.action!==request.action||typeof reply.generation!=='string'||!reply.generation||reply.state!==(reply.action==='start'?'running':'stopped')||reply.action==='start'&&reply.origin!=='http://127.0.0.1:9044'||Object.keys(reply).some(k=>!['sequence','action','generation','state','origin'].includes(k)))throw Error('foreign lifecycle reply');
 fs.writeFileSync('/work/control/reply.partial',JSON.stringify(reply),{mode:0o600});fs.renameSync('/work/control/reply.partial','/work/control/reply.json');
}else throw Error('fixed status/reply action required');
