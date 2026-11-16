// Synthetic transport/permission qualification, never a product acceptance test.
import {readFile,rm,writeFile} from 'node:fs/promises';
import {parseArgs} from 'node:util';
const {values}=parseArgs({options:Object.fromEntries(['source','scratch','patch','reference'].map(name=>[name,{type:'string'}]))});
const html=await readFile(values.source+'/proof.html','utf8');
if(!html.includes('Current captured source'))throw new Error('wrong synthetic source');
let denied=false;
try{await writeFile(values.source+'/forbidden','no');}catch(error){denied=['EACCES','EPERM','EROFS'].includes(error.code);}
if(!denied)throw new Error('read-only source boundary failed');
const cacheProof=process.env.GOCACHE+'/runner-project-fixture-'+process.pid;
try{await writeFile(cacheProof,'shared-cache-proof',{flag:'wx'});if(await readFile(cacheProof,'utf8')!=='shared-cache-proof')throw new Error('shared cache inaccessible');}finally{await rm(cacheProof,{force:true});}
let cacheSiblingDenied=false;
try{await writeFile(process.env.GOCACHE+'.runner-denied-'+process.pid,'no',{flag:'wx'});}catch(error){cacheSiblingDenied=['EACCES','EPERM','EROFS'].includes(error.code);}
if(!cacheSiblingDenied)throw new Error('cache grant escaped its exact directory');
const {chromium}=await import(process.env.RUNNER_PROJECT_PLAYWRIGHT_MODULE);
let browser;
const observations=[];
try {
  browser=await chromium.launch({headless:true,executablePath:process.env.RUNNER_PROJECT_CHROMIUM});
  for(const [name,width,height] of [['phone',390,844],['desktop',1280,900]]) {
    const context=await browser.newContext({viewport:{width,height}});
    try {
      const page=await context.newPage();await page.setContent(html);
      await page.locator('#toggle').click();
      if(await page.locator('#toggle').textContent()!=='On')throw new Error('synthetic interaction failed');
      await page.screenshot({path:values.scratch+'/'+name+'.png'});
      observations.push({name,width,height,toggle:'On'});
    } finally {await context.close();}
  }
  await writeFile(values.scratch+'/observations.json',JSON.stringify({fixture:'Synthetic HTML only; no product acceptance or native visual judgment',browser:browser.version(),source_write_denied:true,shared_cache_writable:true,cache_sibling_denied:true,observations}));
} finally {await browser?.close();}
await writeFile(values.scratch+'/project-result.json',JSON.stringify({status:'passed',reason:'Generic fixed-launcher transport fixture passed; no product acceptance claim.',files:['observations.json','phone.png','desktop.png']}));
