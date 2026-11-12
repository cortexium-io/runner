// Fixed guide-media observations. No repository scripts, selectors, URLs or
// commands are selected by a candidate. This is a separate, not-yet-integrated
// producer contract; the legacy full-puzzle/native gate remains unchanged.
import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import fs from 'node:fs'

export const guideGames = Object.freeze(['sudoku','binary','suguru','nonogram','futoshiki','shikaku','hashi','calcudoku','kakuro'])
export const mediaManifestSHA256 = 'c14f2f4025774775487a4d80ac5914cfdf488cf7ba5278406261475c286e2388'
export class MediaCapabilityError extends Error { name = 'MediaCapabilityError' }
const sha = data => crypto.createHash('sha256').update(data).digest('hex')
const capability = (yes, message) => { if (!yes) throw new MediaCapabilityError(message) }

export function newMediaReceipt(identity, fixture = false) {
  assert.match(identity.source,/^[0-9a-f]{40}$/)
  assert.match(identity.tree,/^[0-9a-f]{40}$/)
  assert.match(identity.image,/^sha256:[0-9a-f]{64}$/)
  return {contract:'runner-guides-browser-v1',manifestSHA256:mediaManifestSHA256,identity,fixture,nativeSafari:'unqualified',physicalSafari:'unverified',independentImageReview:'required',cases:[],images:[],profiles:0}
}

export async function mediaViewport(page, output, receipt, engine, game, state) {
  assert.ok(['chromium','webkit'].includes(engine) && guideGames.includes(game))
  assert.ok(['overview','captions','fallback','poster'].includes(state))
  const {width,height}=page.viewportSize()
  assert.ok([320,390,1440].includes(width))
  const name=`media-${engine}-${width}-${game}-${state}-viewport.png`
  assert.ok(!receipt.images.some(row=>row.name===name) && receipt.images.length<160)
  const png=await page.screenshot({fullPage:false,scale:'css'})
  assert.ok(png.length<=8*1024*1024,'actual viewport image exceeds8MiB')
  assert.deepEqual([png.readUInt32BE(16),png.readUInt32BE(20)],[width,height])
  const total=receipt.images.reduce((n,row)=>n+row.bytes,0)+png.length
  assert.ok(total+Buffer.byteLength(JSON.stringify(receipt))<=16*1024*1024,'bounded media evidence cannot fit16MiB')
  fs.writeFileSync(`${output}/${name}`,png,{flag:'wx',mode:0o600})
  receipt.images.push({name,width,height,bytes:png.length,sha256:sha(png)})
}

async function reach(page, locator) {
  capability(await locator.count()===1,'required media action is not uniquely addressable by its accessible name')
  await locator.scrollIntoViewIfNeeded()
  const hit=await locator.evaluate(node=>{const r=node.getBoundingClientRect(),x=r.x+r.width/2,y=r.y+r.height/2,top=document.elementFromPoint(x,y);return {visible:r.width>0&&r.height>0&&x>=0&&y>=0&&x<innerWidth&&y<innerHeight,hit:node===top||node.contains(top)}})
  assert.ok(hit.visible&&hit.hit,'media action is clipped, covered or outside the actual viewport')
}

async function activate(page, button) { await reach(page,button); await button.click() }
const observe = page => page.locator('#walkthrough video').evaluate(v=>({src:new URL(v.currentSrc,location.href).pathname,poster:new URL(v.poster,location.href).pathname,controls:v.controls,inline:v.playsInline,paused:v.paused,muted:v.muted,volume:v.volume,time:v.currentTime,duration:v.duration,ready:v.readyState,seeking:v.seeking,error:v.error?.code??0,videoFrames:window.runnerGuideDecode?.frames??null,audioPCM:window.runnerGuideDecode?.peak??null,audioState:window.runnerGuideDecode?.context.state??null,tracks:[...v.textTracks].map(t=>({kind:t.kind,language:t.language,mode:t.mode,cues:t.cues?.length??0})),trusted:window.runnerGuideInputWitness}))

// A passive listener observes browser-generated input. It never injects events,
// starts playback, changes progress/storage or fabricates successful state.
async function inputWitness(page) {
  await page.evaluate(()=>{window.runnerGuideInputWitness={clicks:0,keys:0,untrusted:0};for(const type of ['click','keydown'])document.addEventListener(type,e=>{if(e.isTrusted)window.runnerGuideInputWitness[type==='click'?'clicks':'keys']++;else window.runnerGuideInputWitness.untrusted++},{capture:true,passive:true})})
}

async function decoderWitness(page) {
 const supported=await page.locator('#walkthrough video').evaluate(v=>{
  const Audio=window.AudioContext??window.webkitAudioContext
  if(typeof v.requestVideoFrameCallback!=='function'||!Audio)return false
  const context=new Audio(),source=context.createMediaElementSource(v),analyser=context.createAnalyser(),silent=context.createGain(),samples=new Float32Array(256)
  silent.gain.value=0 // Decoder observation is intentionally not speaker proof.
  source.connect(analyser);analyser.connect(silent);silent.connect(context.destination)
  const state={frames:0,peak:0,context,source,analyser,silent,callback:0,resume:undefined}
  const frame=(time,meta)=>{state.frames=meta.presentedFrames;analyser.getFloatTimeDomainData(samples);for(const value of samples)state.peak=Math.max(state.peak,Math.abs(value));state.callback=v.requestVideoFrameCallback(frame)}
  state.resume=e=>{if(e.isTrusted)void context.resume().catch(()=>{})}
  document.addEventListener('click',state.resume,{capture:true,passive:true})
  state.callback=v.requestVideoFrameCallback(frame);window.runnerGuideDecode=state;return true
 })
 capability(supported,'engine lacks presented-frame or media-element PCM observation')
}

export async function settleMediaObservers(page) {
 if(page.isClosed())return
 await page.evaluate(async()=>{const state=window.runnerGuideDecode,v=document.querySelector('#walkthrough video');if(!state)return;if(v)v.cancelVideoFrameCallback(state.callback);document.removeEventListener('click',state.resume,true);state.source.disconnect();state.analyser.disconnect();state.silent.disconnect();await state.context.close();delete window.runnerGuideDecode})
}

export async function observeMediaCase(context, page, origin, receipt, output, engine, game, fallback = false) {
  assert.ok(['chromium','webkit'].includes(engine) && guideGames.includes(game))
  const root=new URL(origin)
  assert.ok(['http:','https:'].includes(root.protocol) && ['127.0.0.1','localhost'].includes(root.hostname) && root.pathname==='/' && !root.username && !root.password)
  page.setDefaultTimeout(5000);page.setDefaultNavigationTimeout(10000)
  assert.ok(++receipt.profiles<=64,'media browser profiles exceed64')
  const path=`/how-to-play/${game}`, asset=`/assets/learning/guides/${game}/`
  await context.route('**/*',route=>{const u=new URL(route.request().url());if(u.origin!==root.origin||!(u.pathname===path||u.pathname.startsWith('/assets/')))return route.abort('blockedbyclient');return route.continue()})
  if(fallback)await context.route(`**${asset}video.mp4`,route=>route.abort('failed'))
  const ranges=[],seekRequests=new Set()
  let seekPhase=false
  page.on('request',request=>{if(seekPhase&&request.url()===root.origin+asset+'video.mp4')seekRequests.add(request)})
  page.on('response',response=>{const u=new URL(response.url());if(u.origin===root.origin&&u.pathname===asset+'video.mp4'&&response.status()===206){const value=response.headers()['content-range'];if(/^bytes \d+-\d+\/\d+$/.test(value??''))ranges.push({value,afterSeek:seekRequests.has(response.request())})}})
  await page.goto(root.origin+path,{waitUntil:'domcontentloaded'})
  assert.equal(new URL(page.url()).pathname,path)
  await inputWitness(page)
  if(fallback){
    await page.getByRole('heading',{name:/transcript/i}).waitFor()
    const transcript=page.getByRole('region',{name:/transcript/i})
    assert.ok(await transcript.innerText(),'fallback lost the readable transcript')
    const notice=page.getByText(/video (?:could not|cannot) (?:load|play)/i)
    await notice.waitFor({state:'visible'})
    await reach(page,transcript)
    await mediaViewport(page,output,receipt,engine,game,'fallback')
    receipt.cases.push({engine,game,fallback:true,transcript:true})
    return
  }
  await page.locator('#walkthrough video').waitFor()
  await page.waitForFunction(()=>document.querySelector('#walkthrough video')?.readyState>=1)
  const initial=await observe(page)
  assert.equal(initial.src,asset+'video.mp4');assert.equal(initial.poster,asset+'poster.png')
  assert.ok(initial.controls&&initial.inline&&initial.paused&&!initial.error,'media initial controls/autoplay state differs')
  assert.equal(initial.tracks.length,1);assert.equal(initial.tracks[0].kind,'captions');assert.equal(initial.tracks[0].mode,'disabled','captions must initially be OFF')
  const transcript=page.getByRole('region',{name:/transcript/i})
  assert.ok((await transcript.innerText()).trim().length>0,'readable transcript missing')
  await page.locator('#walkthrough video').scrollIntoViewIfNeeded()
  await mediaViewport(page,output,receipt,engine,game,'poster')
  const play=page.getByRole('button',{name:'Play',exact:true}),pause=page.getByRole('button',{name:'Pause',exact:true}),captions=page.getByRole('button',{name:'Captions',exact:true}),seek=page.getByRole('slider',{name:'Seek',exact:true})
  await decoderWitness(page)
  await activate(page,play)
  await page.waitForFunction(()=>{const v=document.querySelector('#walkthrough video'),d=window.runnerGuideDecode;return v&&!v.paused&&v.currentTime>0.1&&d.frames>0&&d.peak>0.00001&&d.context.state==='running'})
  const playing=await observe(page)
  capability(playing.audioPCM!==null&&playing.videoFrames!==null,'engine cannot expose actual audio/video decode witnesses')
  assert.ok(playing.videoFrames>0&&playing.audioPCM>0.00001&&!playing.muted&&playing.volume>0,'trusted play did not decode audible-track media')
  await activate(page,pause)
  assert.ok((await observe(page)).paused,'trusted pause did not settle')
  await reach(page,seek)
  seekPhase=true
  const seekResponse=page.waitForResponse(response=>seekRequests.has(response.request())&&response.status()===206&&/^bytes [1-9]\d*-/.test(response.headers()['content-range']??''),{timeout:5000})
  await seek.press('End');await seek.press('ArrowLeft')
  await seekResponse
  await page.waitForFunction(prior=>{const v=document.querySelector('#walkthrough video');return v&&!v.seeking&&v.currentTime>v.duration/2&&window.runnerGuideDecode.frames>prior},playing.videoFrames)
  const sought=await observe(page)
  assert.ok(sought.videoFrames>playing.videoFrames,'seek did not decode a new actual frame')
  assert.ok(ranges.some(row=>row.afterSeek&&Number(row.value.match(/^bytes (\d+)-/)[1])>0),'seek has no actual nonzero Range206 response')
  await activate(page,captions)
  await page.waitForFunction(()=>{const t=document.querySelector('#walkthrough video')?.textTracks[0];return t?.mode==='showing'&&(t.cues?.length??0)>0})
  await reach(page,seek);await seek.press('Home');await activate(page,play)
  await page.waitForFunction(()=>document.querySelector('#walkthrough video')?.textTracks[0]?.activeCues?.length>0)
  await activate(page,pause)
  const showing=await observe(page)
  assert.equal(await captions.getAttribute('aria-pressed'),'true')
  await mediaViewport(page,output,receipt,engine,game,'captions')
  await activate(page,captions);assert.equal((await observe(page)).tracks[0].mode,'disabled')
  assert.equal(await captions.getAttribute('aria-pressed'),'false')
  const views=[]
  for(const width of [320,390,1440]){
    await page.setViewportSize({width,height:width===1440?900:844})
    for(const button of [play,pause,captions])await reach(page,button)
    await reach(page,seek)
    await page.locator('#walkthrough video').scrollIntoViewIfNeeded()
    const layout=await page.evaluate(()=>({width:innerWidth,documentWidth:document.documentElement.scrollWidth}))
    assert.ok(layout.documentWidth<=layout.width+1,'media page horizontally overflows the actual viewport')
    await mediaViewport(page,output,receipt,engine,game,'overview');views.push(width)
  }
  const final=await observe(page)
  assert.ok(final.trusted.clicks>=4&&final.trusted.keys>=2&&final.trusted.untrusted===0,'media evidence is missing settled trusted UI input: '+JSON.stringify(final.trusted))
  receipt.cases.push({engine,game,views,src:initial.src,poster:initial.poster,duration:playing.duration,decode:{frames:playing.videoFrames,pcmPeak:playing.audioPCM,method:'presented-frame callback and pre-zero-gain media-element PCM analyser; no speaker proof'},pause:true,seek:{time:sought.time,range:ranges.find(row=>row.afterSeek&&Number(row.value.match(/^bytes (\d+)-/)[1])>0)?.value},captions:['disabled','showing','disabled'],cueCount:showing.tracks[0].cues,trusted:final.trusted,transcript:true})
  assert.ok(Buffer.byteLength(JSON.stringify(receipt))<=128*1024,'media receipt exceeds128KiB')
}
