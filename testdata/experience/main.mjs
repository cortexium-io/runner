// Disposable fixture only. Captures Chrome for Testing's actual viewport and
// trusted emulated touch. It supplies no product or native Safari acceptance.
import fs from 'node:fs'
import crypto from 'node:crypto'
import zlib from 'node:zlib'
import path from 'node:path'
import {pathToFileURL} from 'node:url'
const [module, executable, output] = process.argv.slice(2)
if (![module,executable,output].every(p=>p&&path.isAbsolute(p))) throw Error('Explicit installed runtime and owned output paths required')
fs.mkdirSync(output,{recursive:true,mode:0o700})
const {chromium}=await import(pathToFileURL(module))
const browser=await chromium.launch({headless:true,executablePath:executable})
const files=new Map(), images=[]
const hash=b=>crypto.createHash('sha256').update(b).digest('hex')
const receipt={key:'fixture_qualification',run_id:'fixture_run',action:'qualify',policy_sha256:'a'.repeat(64),source_ref:'b'.repeat(40)}
const interactions=[]
try{
  const context=await browser.newContext({viewport:{width:640,height:360},hasTouch:true,isMobile:true,deviceScaleFactor:1})
  const page=await context.newPage()
  for(const engine of ['chromium','webkit'])for(const device of ['desktop','mobile'])for(const game of ['sudoku','suguru','nonogram','binary','futoshiki','shikaku','hashi','calcudoku','kakuro']){
    // Names exercise the fixed binding matrix. The manifest below explicitly
    // labels the actual fixture engine: these are not WebKit/device claims.
    const blocked=engine==='webkit'&&device==='mobile'&&game==='shikaku'
    await page.setContent(`<meta name="viewport" content="width=device-width,initial-scale=1"><style>*{box-sizing:border-box}body{margin:16px;font:20px system-ui;background:#faf8f1}h1{font-size:24px;margin:0 0 8px}#board{width:320px;height:240px;background:repeating-linear-gradient(90deg,transparent 0 39px,#576550 39px 40px),repeating-linear-gradient(0deg,#d1dfc3 0 39px,#576550 39px 40px);border:2px solid #344a36}button{font:20px system-ui;padding:12px;margin-right:8px;background:white;border:2px solid #344a36;border-radius:6px}#controls{position:absolute;left:360px;top:90px;width:250px}#cover{position:absolute;left:350px;top:160px;width:270px;height:120px;background:#bf2525;color:white;font-weight:bold;padding:18px;z-index:2}</style><h1>Puzzle fixture · ${game}</h1><div id="board" role="grid" aria-label="Board"></div><div id="controls"><button onclick="this.textContent='Selected'">Notes</button><p>Editing controls</p><button>Undo</button><button>Redo</button></div>${blocked?'<div id="cover">Privacy panel covers editing controls</div>':''}`)
    await page.evaluate(()=>{window.observed=[];document.addEventListener('pointerdown',e=>window.observed.push({trusted:e.isTrusted,type:e.pointerType}),true)})
    await page.getByRole('button',{name:'Notes',exact:true}).tap()
    interactions.push({binding:engine+'-'+device+'-'+game,actualEngine:'chromium',evidenceMode:'emulated touch fixture',events:await page.evaluate(()=>window.observed),selected:await page.getByRole('button',{name:'Selected',exact:true}).count()===1})
    const name=`${engine}-${device}-${game}-viewport.png`
    const png=await page.screenshot({fullPage:false,scale:'css'})
    files.set(name,png);images.push({name,sha256:hash(png),width:640,height:360})
  }
  files.set('browser.json',Buffer.from(JSON.stringify({fixture:true,actualEngine:'Chromium Chrome for Testing',interactionEvidence:interactions,sourceChecks:'passed',nativeSafari:'unqualified'})))
  receipt.browser_evidence_sha256=hash(files.get('browser.json'))
  files.set('experience.json',Buffer.from(JSON.stringify({version:1,run_id:receipt.run_id,qualification_key:receipt.key,receipt_sha256:hash(JSON.stringify(receipt)),head:'c'.repeat(40),image:'sha256:'+'d'.repeat(64),server_sha256:'e'.repeat(64),browser_sha256:hash(files.get('browser.json')),images,native_safari:'unqualified',physical_safari:'unverified'})))
  const parts=[]
  for(const [name,body] of [...files].sort(([a],[b])=>a.localeCompare(b))){
    const h=Buffer.alloc(512);h.write(name)
    const oct=(n,o,l)=>h.write(n.toString(8).padStart(l-1,'0')+'\0',o,l)
    oct(0o600,100,8);oct(0,108,8);oct(0,116,8);oct(body.length,124,12);oct(0,136,12);h.fill(32,148,156);h.write('0',156);h.write('ustar\0',257);h.write('00',263);h.write([...h].reduce((a,b)=>a+b,0).toString(8).padStart(6,'0')+'\0 ',148,8)
    parts.push(h,body,Buffer.alloc((512-body.length%512)%512))
  }
  parts.push(Buffer.alloc(1024))
  fs.writeFileSync(path.join(output,'fixture.tar.gz'),zlib.gzipSync(Buffer.concat(parts)),{mode:0o600})
  fs.writeFileSync(path.join(output,'receipt.json'),JSON.stringify(receipt),{mode:0o600})
  fs.writeFileSync(path.join(output,'capture.json'),JSON.stringify({browser:await browser.version(),physical:false,nativeSafari:false,interactions}),{mode:0o600})
}finally{await browser.close()}
