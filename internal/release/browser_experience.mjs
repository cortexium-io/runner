import assert from 'node:assert/strict'
import crypto from 'node:crypto'

// Fixed observations only. Neither a candidate nor task data selects scripts,
// URLs, filenames or commands. These images describe actual viewport states;
// they do not replace the independent image judgment or interaction obligations.
export async function captureViewport(receipt, output, page, engine, device, game, state = '') {
  const name = `${engine}-${device}-${game.toLowerCase()}${state ? '-' + state : ''}-viewport.png`
  assert.match(name,/^(chromium|webkit)-(desktop|mobile)-(sudoku|suguru|nonogram|binary|futoshiki|shikaku|hashi|calcudoku|kakuro)(-(chooser-dark|initial|largest-light|landscape|tablet-phone|hint))?-viewport\.png$/)
  assert.ok(receipt.viewportImages.length < 160 && !receipt.viewportImages.some(row=>row.name===name), 'viewport evidence exceeds its fixed matrix')
  const png = await page.screenshot({path:`${output}/${name}`,fullPage:false,scale:'css'})
  assert.ok(png.length<=8*1024*1024,'viewport image exceeds allowance')
  receipt.viewportImages.push({name,sha256:crypto.createHash('sha256').update(png).digest('hex'),width:png.readUInt32BE(16),height:png.readUInt32BE(20)})
  const observed=await page.evaluate(()=>({viewport:[innerWidth,innerHeight],visualScale:visualViewport?.scale,scroll:[scrollX,scrollY],documentWidth:document.documentElement.scrollWidth,input:window.runnerInputEvidence||null}))
  // Index binds to viewportImages without repeating its long filename/state or
  // pixel dimensions, keeping the full matrix in the unchanged receipt bound.
  receipt.experienceObservations.push({image_index:receipt.viewportImages.length-1,path:new URL(page.url()).pathname,...observed})
  return name
}

export const settleViewport = page => page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))))
