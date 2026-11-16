import {readFile,writeFile,readdir,open} from 'node:fs/promises';
import {createHash} from 'node:crypto';
const [source,scratch,patch,otherState]=process.argv.slice(2),sha=b=>createHash('sha256').update(b).digest('hex');
const html=await readFile(source+'/proof.html','utf8');
if(!html.includes('Current captured source'))throw new Error('wrong candidate source');
for(const action of [()=>writeFile(source+'/forbidden','forbidden'),()=>readFile(otherState),()=>readdir(otherState.substring(0,otherState.lastIndexOf('/')))]) {
 let denied=false;try{await action();}catch(e){denied=['EACCES','EPERM','EROFS'].includes(e.code);}if(!denied)throw new Error('source/state boundary failed');
}
if(process.env.RUNNER_SOURCE_PROOF_SYNC==='1') {
 const ready=await open(scratch+'/events','w');try{await ready.write(Buffer.from('R'));}finally{await ready.close();}
 const gate=await open(scratch+'/gate','r');try{await gate.read(Buffer.alloc(1),0,1,null);}finally{await gate.close();}
}
const report={source_readonly:true,other_state_denied:true,patch_sha256:sha(await readFile(patch)),html_sha256:sha(Buffer.from(html)),images:[],browser:'not_requested'};
if(process.env.RUNNER_SOURCE_PROOF_PLAYWRIGHT) {
 const {chromium}=await import(process.env.RUNNER_SOURCE_PROOF_PLAYWRIGHT);
 let browser;try {
  browser=await chromium.launch({headless:true,executablePath:process.env.RUNNER_SOURCE_PROOF_CHROME});
  for(const [name,width,height] of [['phone',390,844],['desktop',1280,900]]) {
   const context=await browser.newContext({viewport:{width,height}});
   try {const page=await context.newPage();await page.setContent(html);await page.locator('#toggle').click();if(await page.locator('#toggle').textContent()!=='On')throw new Error('generic interaction failed');const bytes=await page.screenshot({path:scratch+'/'+name+'.png'});report.images.push({name:name+'.png',width,height,sha256:sha(bytes)});}finally{await context.close();}
  }
  report.browser=browser.version();
 }finally{await browser?.close();}
}
await writeFile(scratch+'/report.json',JSON.stringify(report));
await writeFile(scratch+'/domain.json',JSON.stringify({outcome:'ready',reason:'Generic source-bound read-only fixture passed; no product acceptance claim.'}));
