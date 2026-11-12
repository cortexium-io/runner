// Operator maintenance only: prepare public runtime bytes, never App source,
// npm/Genny/Go builds, credentials or an execution surface for product roles.
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import {fileURLToPath} from 'node:url';
const [context,vendor,modules]=process.argv.slice(2);
if(!context||!vendor||!modules||![context,vendor,modules].every(p=>path.isAbsolute(p)))throw Error('new owned context and pinned public runtime/module roots required');
fs.mkdirSync(context,{mode:0o700});
fs.mkdirSync(context+'/app-base',{mode:0o755});
fs.writeFileSync(context+'/app-base/.keep','',{mode:0o444});
const source=path.dirname(fileURLToPath(import.meta.url));
const hash=data=>crypto.createHash('sha256').update(data).digest('hex');
function fileHash(file){const h=crypto.createHash('sha256'),fd=fs.openSync(file,'r'),b=Buffer.alloc(256*1024);try{for(;;){const n=fs.readSync(fd,b,0,b.length,null);if(!n)break;h.update(b.subarray(0,n));}}finally{fs.closeSync(fd);}return h.digest('hex');}
const node=vendor+'/node-v26.13.1-linux-x64/bin/node',chrome=vendor+'/chrome-linux64/chrome';
for(const [file,expected]of [[node,'d931c649850ffa7155b8ceffb400dc916ef325866e509cc52ae36e33e8fbb9a9'],[chrome,'76bcde21a2f8c9d4fbbf42afbeab011817d000552d093e5f5e89c9b8808f4231']])if(fileHash(file)!==expected)throw Error('pinned vendor runtime bytes differ');
fs.copyFileSync(node,context+'/node');
fs.chmodSync(context+'/node',0o555);
fs.cpSync(vendor+'/chrome-linux64',context+'/chrome-linux64',{recursive:true});
fs.mkdirSync(context+'/node_modules');
for(const name of ['playwright','playwright-core']){
  const metadata=JSON.parse(fs.readFileSync(modules+'/'+name+'/package.json'));
  if(metadata.version!=='1.65.0')throw Error('public Playwright package differs');
  fs.cpSync(modules+'/'+name,context+'/node_modules/'+name,{recursive:true});
}
for(const name of ['Dockerfile','package.json','package-lock.json'])fs.copyFileSync(source+'/'+name,context+'/'+name);
for(const name of ['browser.mjs','container.mjs','control.mjs']){fs.copyFileSync(source+'/../'+name,context+'/'+name);fs.chmodSync(context+'/'+name,0o444);}
const driver=hash(Buffer.from(['browser.mjs','container.mjs','control.mjs'].map(name=>name+':'+hash(fs.readFileSync(context+'/'+name))+'\n').join('')));
const files=[];
function scan(root,relative=''){
  for(const e of fs.readdirSync(root,{withFileTypes:true})){
    const file=path.join(root,e.name),name=path.join(relative,e.name);
    if(e.isDirectory())scan(file,name);
    else if(e.isFile()){files.push({name,bytes:fs.statSync(file).size,sha256:fileHash(file)});}
    else throw Error('runtime context must contain ordinary files only: '+name);
  }
}
scan(context);files.sort((a,b)=>a.name.localeCompare(b.name));
fs.writeFileSync(context+'/runtime-files.json',JSON.stringify({version:1,driver_sha256:driver,node:'26.13.1',playwright:'1.65.0',full_cft:'158.0.8127.18',files}),{mode:0o600});
console.log(JSON.stringify({context,driver_sha256:driver,regular_files:files.length,total_bytes:files.reduce((s,f)=>s+f.bytes,0)}));
