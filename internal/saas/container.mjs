// Fixed runtime-only entrypoint. App/driver paths and output names are installed
// bytes; neither candidate code nor the request selects commands or resources.
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import {spawn,spawnSync} from 'node:child_process';
import {createRequire} from 'node:module';

const require=createRequire('/runner/package.json');
const chrome='/runtime/chrome-linux64/chrome';
const node='/usr/local/bin/node';
const hash=buffer=>crypto.createHash('sha256').update(buffer).digest('hex');
function fileHash(file){
  const h=crypto.createHash('sha256'),fd=fs.openSync(file,'r'),b=Buffer.alloc(256*1024);
  try{for(;;){const n=fs.readSync(fd,b,0,b.length,null);if(!n)break;h.update(b.subarray(0,n));}}finally{fs.closeSync(fd);}
  return h.digest('hex');
}
const driver=hash(Buffer.from(['browser.mjs','container.mjs','control.mjs'].map(name=>name+':'+fileHash('/runner/'+name)+'\n').join('')));
if(driver!==process.env.RUNNER_CORTEXIUM_DRIVER_SHA256||process.version!=='v26.13.1'||require('playwright/package.json').version!=='1.65.0')throw Error('fixed runtime/driver differs');
const runtime={version:1,node:process.version,playwright:require('playwright/package.json').version,driver_sha256:driver,chrome_sha256:fileHash(chrome)};
const argv=process.argv.slice(2);
if(argv.length===1&&argv[0]==='--runtime-info'){
  const result=spawnSync(chrome,['--version'],{env:{PATH:'/usr/bin:/bin',HOME:'/work',TMPDIR:'/tmp'},encoding:'utf8',timeout:15000,maxBuffer:4096});
  if(result.status!==0||!result.stdout.includes('158.0.8127.18'))throw Error('full CFT version differs');
  process.stdout.write(JSON.stringify({...runtime,chrome_version:result.stdout.trim(),browser_kind:'full Chrome for Testing linux64'}));
}else{
  if(argv.length)throw Error('fixed entrypoint accepts no App arguments');
  const manifest=JSON.parse(fs.readFileSync('/app/manifest.json','utf8'));
  if(manifest.version!==1||manifest.evidence?.server_sha256!==fileHash('/app/server')||manifest.evidence?.build?.target!=='linux/amd64/v1'||manifest.evidence?.build?.runtime?.driver_sha256!==driver||manifest.evidence?.build?.runtime?.chrome_sha256!==runtime.chrome_sha256)throw Error('reviewed image bytes/runtime differ');
  fs.mkdirSync('/work/viewports',{recursive:true,mode:0o700});
  const child=spawn(node,['/runner/browser.mjs','/runner',chrome,'/work','/work/viewports'],{
    cwd:'/app',env:{PATH:'/usr/local/bin:/usr/bin:/bin',HOME:'/work',TMPDIR:'/tmp',OPENSSL_CONF:'/dev/null'},stdio:['ignore','pipe','pipe'],
  });
  let diagnosticBytes=0,keptBytes=0;const chunks=[];
  for(const pipe of [child.stdout,child.stderr])pipe.on('data',data=>{
    diagnosticBytes+=data.length;const kept=data.subarray(0,Math.max(0,65536-keptBytes));
    if(kept.length){chunks.push(kept);keptBytes+=kept.length;}
    if(diagnosticBytes>65536)child.kill('SIGTERM');
  });
  let killing=false;
  const stop=()=>{if(killing)return;killing=true;child.kill('SIGTERM');const force=setTimeout(()=>{if(child.exitCode===null&&child.signalCode===null)child.kill('SIGKILL');},5000);force.unref();};process.once('SIGTERM',stop);process.once('SIGINT',stop);
  const timeout=setTimeout(stop,17*60*1000);
  const cancelWatch=setInterval(()=>{if(fs.existsSync('/work/cancel'))stop();},250);
  let code,signal;
  try{[code,signal]=await new Promise((resolve,reject)=>{child.once('error',reject);child.once('close',(c,s)=>resolve([c,s]));});}finally{clearTimeout(timeout);clearInterval(cancelWatch);}
  const log=Buffer.concat(chunks);
  const receipt={...runtime,child_exit:code,child_signal:signal,diagnostics:log.toString('utf8'),diagnostic_bytes:diagnosticBytes,truncated:diagnosticBytes>keptBytes};
  fs.writeFileSync('/work/viewports/runtime.json',JSON.stringify(receipt),{mode:0o600});
  const image=/^chrome-(desktop|narrow)-(setup|run|revision|accepted|failure|tenant-denied|grant-denied|cancelled)-viewport\.png$/;
  const names=fs.readdirSync('/work/viewports').filter(name=>image.test(name)||['observations.json','cleanup.json','runtime.json'].includes(name));
  let total=0;
  for(const name of names){const p=path.join('/work/viewports',name),s=fs.lstatSync(p);if(!s.isFile()||s.isSymbolicLink()||s.size>(image.test(name)?8*1024*1024:128*1024))throw Error('output exceeds fixed regular-file contract');total+=s.size;}
  if(total>16*1024*1024||names.length>163)throw Error('viewport export exceeds16MiB');
  // Only the fixed observations/diagnostics/viewport names leave the container.
  // SQLite, cookies, Chrome profile and unrelated files never enter this list.
  const archive=spawnSync('/bin/tar',['--format=ustar','--blocking-factor=1','-czf','-','-C','/work/viewports',...names],{maxBuffer:16*1024*1024,env:{PATH:'/usr/bin:/bin'}});
  if(archive.status!==0||archive.error)throw Error('bounded viewport export failed');
  fs.writeFileSync('/work/evidence.tar.gz',archive.stdout,{mode:0o600});
  fs.writeFileSync('/work/result.json',JSON.stringify({done:true,bytes:archive.stdout.length,sha256:hash(archive.stdout)}),{mode:0o600});
  // Keep the bounded owned tmpfs readable until trusted operator export/cleanup.
  // The separate anchor also retains it across producer-container failure.
  setInterval(()=>{},60000);
}
