// Disposable neutral media fixture: no product source, approved video re-encode,
// personal browser, Docker, model or Safari/simulator session is used.
import fs from 'node:fs'
import path from 'node:path'
import os from 'node:os'
import http from 'node:http'
import {spawnSync} from 'node:child_process'
import {once} from 'node:events'
import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import {guideGames,newMediaReceipt,observeMediaCase,settleMediaObservers} from '../../internal/release/guides_browser.mjs'

const output=process.argv[2]
assert.ok(output&&path.isAbsolute(output)&&!fs.existsSync(output),'fresh owned evidence directory required')
fs.mkdirSync(output,{mode:0o700})
const temp=fs.mkdtempSync(path.join(os.tmpdir(),'runner-guides-media-'))
let server,browser,activeMode='positive',receipt
const versions={}
const sockets=new Set(),videoRequests=[]
let allowSeekRanges=false
const hash=data=>crypto.createHash('sha256').update(data).digest('hex')
const html=game=>`<!doctype html><html lang="en"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Neutral media producer fixture</title><style>body{margin:12px;font:16px system-ui;max-width:900px}video{width:100%;height:auto}button,input{min-height:44px}button{margin:3px}input{width:90%}section{scroll-margin:12px}</style><h1>Neutral ${game} media fixture</h1><section id="walkthrough" aria-label="Walkthrough"><video controls playsinline preload="metadata" poster="/assets/learning/guides/${game}/poster.png" aria-label="Neutral generated sample"><source src="/assets/learning/guides/${game}/video.mp4" type="video/mp4"><track kind="captions" srclang="en" label="English" src="/assets/learning/guides/${game}/captions.vtt" ${activeMode==='captions-on'?'default':''}></video><p id="error" hidden>The video could not load. Read the transcript below.</p><button id="play">Play</button><button id="pause">Pause</button><button id="captions" aria-pressed="false">Captions</button><label>Seek<input type="range" min="0" max="100" value="0" aria-label="Seek"></label><section aria-label="Transcript"><h2>Text transcript</h2><p>A neutral generated counter and sine wave qualify the observer. This is no product or narration QA.</p></section></section><script>const v=document.querySelector('video');document.querySelector('#play').onclick=()=>v.play();document.querySelector('#pause').onclick=()=>v.pause();document.querySelector('#captions').onclick=e=>{const on=v.textTracks[0].mode!=='showing';v.textTracks[0].mode=on?'showing':'disabled';e.currentTarget.setAttribute('aria-pressed',String(on))};document.querySelector('input').oninput=async e=>{const value=Number(e.target.value);await fetch('/assets/fixture-seek');v.currentTime=v.duration*value/100};v.onerror=()=>{document.querySelector('#error').hidden=false;v.hidden=true};for(const source of v.querySelectorAll('source'))source.onerror=v.onerror;${activeMode==='captions-on'?"v.addEventListener('loadedmetadata',()=>{v.textTracks[0].mode='showing'})":''}</script></html>`
try {
  const generated=spawnSync('/opt/homebrew/bin/ffmpeg',['-nostdin','-v','error','-f','lavfi','-i','testsrc2=size=320x180:rate=15','-f','lavfi','-i','sine=frequency=440:sample_rate=44100','-t','30','-c:v','libx264','-b:v','1500k','-minrate','1500k','-maxrate','1500k','-bufsize','1500k','-x264-params','nal-hrd=cbr:force-cfr=1','-c:a','aac','-b:a','64k','-movflags','+faststart',path.join(temp,'neutral.mp4')],{timeout:60000,maxBuffer:8192})
  assert.equal(generated.status,0,generated.stderr?.toString())
  const video=fs.readFileSync(path.join(temp,'neutral.mp4'))
  const painted=spawnSync('/opt/homebrew/bin/ffmpeg',['-nostdin','-v','error','-f','lavfi','-i','color=c=0x225599:s=320x180','-frames:v','1',path.join(temp,'neutral.png')],{timeout:60000,maxBuffer:8192});assert.equal(painted.status,0,painted.stderr?.toString());const poster=fs.readFileSync(path.join(temp,'neutral.png'))
  server=http.createServer((req,res)=>{
    if(req.url==='/assets/fixture-seek'){allowSeekRanges=true;res.end('fixture transport gate only');return}
    const route=req.url.match(/^\/how-to-play\/(sudoku|binary|suguru|nonogram|futoshiki|shikaku|hashi|calcudoku|kakuro)$/)
    if(route){allowSeekRanges=false;res.setHeader('Content-Type','text/html; charset=utf-8');res.end(html(route[1]));return}
    const asset=req.url.match(/^\/assets\/learning\/guides\/[a-z]+\/(video.mp4|poster.png|captions.vtt)$/)
    if(!asset){res.writeHead(404);res.end();return}
    if(asset[1]==='poster.png'){res.setHeader('Content-Type','image/png');res.end(poster);return}
    if(asset[1]==='captions.vtt'){res.setHeader('Content-Type','text/vtt');res.end('WEBVTT\n\n00:00.000 --> 00:30.000\nNeutral producer fixture.\n');return}
    res.setHeader('Content-Type','video/mp4');res.setHeader('Accept-Ranges','bytes');res.setHeader('Cache-Control','no-store')
    const range=req.headers.range?.match(/^bytes=(\d+)-(\d*)$/)
    if(!range){res.end(video);return}
    const start=Number(range[1]);let end=range[2]?Math.min(Number(range[2]),video.length-1):video.length-1
    if(!allowSeekRanges&&start<1048576)end=Math.min(end,1048575)
    if(start> end){res.writeHead(416);res.end();return}
    videoRequests.push({start,end,allowSeekRanges});if(videoRequests.length>256)videoRequests.shift()
    res.writeHead(206,{'Content-Range':`bytes ${start}-${end}/${video.length}`,'Content-Length':end-start+1})
    // Hold the initial stream after enough bytes to decode. A real nonzero
    // browser seek must request another range; no arbitrary timing sleep.
    if(!allowSeekRanges&&start>=1048576)return
    res.end(video.subarray(start,end+1))
  })
  server.on('connection',socket=>{sockets.add(socket);socket.once('close',()=>sockets.delete(socket))})
  server.listen(0,'127.0.0.1');await once(server,'listening')
  const origin=`http://127.0.0.1:${server.address().port}`
  const playwright=await import(process.env.RUNNER_GUIDE_PLAYWRIGHT)
  receipt=newMediaReceipt({source:'1'.repeat(40),tree:'2'.repeat(40),image:'sha256:'+'3'.repeat(64)},true)
  for(const engine of ['chromium','webkit']){
    browser=await playwright[engine].launch({headless:true,...(engine==='chromium'?{executablePath:'/Users/dan/.codex/tools/browser-runtime/chrome'}:{})})
    versions[engine]=browser.version()
    for(const game of guideGames){
      let context=await browser.newContext({viewport:{width:390,height:844},deviceScaleFactor:1})
      try {const page=await context.newPage();try {await observeMediaCase(context,page,origin,receipt,output,engine,game)}catch(error){receipt.lastState=await page.evaluate(()=>{const v=document.querySelector('video');return {document:document.readyState,fonts:document.fonts.status,paused:v?.paused,time:v?.currentTime,ready:v?.readyState,error:v?.error?.code,buffered:v?[...Array(v.buffered.length)].map((_,i)=>[v.buffered.start(i),v.buffered.end(i)]):[]}});throw error}} finally {for(const page of context.pages())await settleMediaObservers(page);await context.close()}
      context=await browser.newContext({viewport:{width:390,height:844},deviceScaleFactor:1})
      try {await observeMediaCase(context,await context.newPage(),origin,receipt,output,engine,game,true)} finally {for(const page of context.pages())await settleMediaObservers(page);await context.close()}
    }
    // A meaningful negative: the same actual UI starts with captions ON.
    // The observer must refuse, with no inference from an expected screenshot.
    activeMode='captions-on'
    const context=await browser.newContext({viewport:{width:390,height:844},deviceScaleFactor:1})
    try {const page=await context.newPage();const negative=newMediaReceipt(receipt.identity,true);await assert.rejects(()=>observeMediaCase(context,page,origin,negative,output,engine,'sudoku'),/captions must initially be OFF/)} finally {for(const page of context.pages())await settleMediaObservers(page);await context.close();activeMode='positive'}
    await browser.close();browser=undefined
  }
  assert.equal(receipt.cases.length,36);assert.equal(receipt.images.length,108)
  const report={...receipt,complete:true,negativeCases:['chromium captions initially ON refused','webkit captions initially ON refused'],input:'trusted browser mouse/keyboard; viewport emulation only',versions,driverSHA256:hash(fs.readFileSync(new URL('../../internal/release/guides_browser.mjs',import.meta.url))),neutralVideoBytes:video.length,source:'synthetic neutral fixture; not product/candidate/native Safari proof'}
  const data=JSON.stringify(report,null,2);assert.ok(Buffer.byteLength(data)<=128*1024);assert.ok(Buffer.byteLength(data)+receipt.images.reduce((n,row)=>n+row.bytes,0)<=16*1024*1024);fs.writeFileSync(path.join(output,'receipt.json'),data,{flag:'wx',mode:0o600})
} catch(error) {
 if(receipt)fs.writeFileSync(path.join(output,'partial-receipt.json'),JSON.stringify({...receipt,complete:false,versions,failure:error.name+': '+error.message,videoRequests},null,2),{flag:'wx',mode:0o600})
 throw error
} finally {
  if(browser)await browser.close()
  for(const socket of sockets)socket.destroy()
  if(server)await new Promise(resolve=>server.close(resolve))
  fs.rmSync(temp,{recursive:true,force:false})
}
