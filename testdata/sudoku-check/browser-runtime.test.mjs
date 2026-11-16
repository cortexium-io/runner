import {test} from 'node:test';
import assert from 'node:assert/strict';
import {pathToFileURL} from 'node:url';
import {chromiumFocusDriver,paintedLockedDigitFacts,assertExhaustedHighlight} from '../../cmd/runner-sudoku-check/browser-contract.mjs';

// Opt-in, model-free qualification using the configured headless runtimes.
// No application, source tree, server, persistent profile or network is used.
test('configured browser focus and final-paint helpers',{
  skip:process.env.RUNNER_SUDOKU_BROWSER_RUNTIME!=='1',
  timeout:20000,
},async t=>{
  for(const name of ['PLAYWRIGHT','CHROMIUM','WEBKIT'])assert.ok(process.env['RUNNER_SUDOKU_'+name],`configured ${name} runtime is required`);
  const {chromium,webkit}=await import(pathToFileURL(process.env.RUNNER_SUDOKU_PLAYWRIGHT).href);
  const contextOptions={viewport:{width:390,height:844},isMobile:true,hasTouch:true,reducedMotion:'reduce'};
  for(const [engine,type] of [['chromium',chromium],['webkit',webkit]]) {
    const browser=await type.launch({executablePath:process.env['RUNNER_SUDOKU_'+engine.toUpperCase()],headless:true});
    try {
      if(engine==='chromium')await t.test('trusted visible blur/focus and cleanup after an interrupted operation',async()=>{
        const context=await browser.newContext(contextOptions);
        try {
          const page=await context.newPage();await page.clock.install();
          await page.setContent('<title>Owned focus fixture</title><button>Fixture</button>');
          const driver=await chromiumFocusDriver(page),boundaries=[];
          await page.evaluate(()=>{
            window.events=[];
            for(const type of ['blur','focus'])window.addEventListener(type,event=>window.events.push({type,trusted:event.isTrusted,focused:document.hasFocus(),visibility:document.visibilityState}));
          });
          const state=()=>page.evaluate(()=>({focused:document.hasFocus(),visibility:document.visibilityState}));
          await assert.rejects((async()=>{
            try {
              for(let cycle=0;cycle<3;cycle++) {
                const before=await state();await driver.away();const lost=await state();
                await page.clock.runFor(100);await driver.back();const returned=await state();
                assert.deepEqual(before,{focused:true,visibility:'visible'});
                assert.deepEqual(lost,{focused:false,visibility:'visible'});
                assert.deepEqual(returned,{focused:true,visibility:'visible'});
                boundaries.push({before,lost,returned});
              }
              const events=await page.evaluate(()=>window.events);
              assert.equal(events.length,6);
              for(let i=0;i<events.length;i++)assert.deepEqual(events[i],{type:i%2?'focus':'blur',trusted:true,focused:!!(i%2),visibility:'visible'});
              t.diagnostic(JSON.stringify({engine,boundaries,events}));
              await driver.away();throw Error('fixture operation failed');
            } finally {await driver.close()}
          })(),/fixture operation failed/);
          assert.equal(context.pages().length,1,'owned focus page survived failed-operation cleanup');
          assert.deepEqual(await state(),{focused:true,visibility:'visible'});
        } finally {await context.close()}
      });
      await t.test(engine+' observes final locked and disabled colors after real CSS transitions',async()=>{
        const context=await browser.newContext(contextOptions);
        try {
          const page=await context.newPage();
          await page.setContent(`<style>
            button{background:rgb(241,244,250);color:rgb(30,40,50);transition:background-color 180ms linear,color 180ms linear}
            button.inspecting{background:rgb(221,231,179)}
            button.exhausted span{opacity:.65}
          </style><button><span>9</span></button>`);
          const digit=page.locator('button');
          const pending=await digit.evaluate(node=>{
            const neutral=getComputedStyle(node).backgroundColor;
            node.classList.add('inspecting');
            return {neutral,transitioning:node.getAnimations().length};
          });
          assert.ok(pending.transitioning>0,'fixture did not start its actual color transition');
          const before=await paintedLockedDigitFacts(digit);
          assert.equal(before.background,'rgb(221, 231, 179)');
          await digit.evaluate(node=>{node.disabled=true;node.classList.add('exhausted')});
          const after=await paintedLockedDigitFacts(digit);
          assert.equal(assertExhaustedHighlight(before,after,9).lime_retained,true);
          t.diagnostic(JSON.stringify({engine,pending,before,after}));
        } finally {await context.close()}
      });
    } finally {
      await browser.close();
      t.diagnostic(JSON.stringify({engine,contexts:browser.contexts().length,browser_connected:browser.isConnected()}));
    }
  }
});
