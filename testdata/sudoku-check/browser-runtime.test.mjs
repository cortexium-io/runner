import {test} from 'node:test';
import assert from 'node:assert/strict';
import {pathToFileURL} from 'node:url';
import {chromiumFocusDriver,paintedLockedDigitFacts,assertExhaustedHighlight,feedbackLayoutFacts,assertFeedbackLayout,guideControlFacts,visitSudokuGuide,assertSudokuGuide} from '../../cmd/runner-sudoku-check/browser-contract.mjs';

// Opt-in, model-free qualification using the configured headless runtimes.
// No application, source tree, server, persistent profile or network is used.
test('configured browser focus, paint and play-header helpers',{
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
      await t.test(engine+' measures a right-side play-header group independently of its fitted board',async()=>{
        const context=await browser.newContext({...contextOptions,viewport:{width:320,height:568}});
        try {
          const page=await context.newPage();
          await page.setContent(`<style>
            body{margin:0}.sudoku-play{width:304px;margin:8px}
            .play-heading{box-sizing:border-box;width:304px;display:flex;align-items:center;border:2px solid;padding:4px}
            .play-heading h1{margin:0;font-size:14px}.actions{display:flex;gap:6px;margin-left:auto}
            .actions button{box-sizing:border-box;width:62px;height:32px}.actions button:last-child{width:70px}
            .player-board-frame{width:280px;height:280px;margin:10px auto}.puzzle-board{height:100%}
            .input-tools button{height:52px}button:disabled{pointer-events:none}
          </style><main class="sudoku-play">
            <header class="play-heading"><h1>Sudoku</h1><div class="actions"><button>Check</button><button>Restart</button></div></header>
            <div class="player-board-frame"><div class="puzzle-board"></div></div>
            <div class="input-tools"><button>Undo</button></div>
          </main>`);
          const facts=await page.evaluate(feedbackLayoutFacts),proof=assertFeedbackLayout(facts);
          assert.equal(facts.heading.runs.length,1,'actual title text Range did not produce one line');
          assert.equal(facts.header_content.x,14);assert.equal(facts.header_content.right,306);
          assert.ok(proof.action_group.right>facts.frame.right+1,'fixture did not extend past the narrower fitted board');
          const check=page.getByRole('button',{name:'Check',exact:true});
          await check.evaluate(node=>{
            const r=node.getBoundingClientRect(),cover=document.createElement('div');cover.id='owned-cover';
            Object.assign(cover.style,{position:'fixed',left:r.x+'px',top:r.y+'px',width:r.width+'px',height:r.height+'px',zIndex:10});
            document.body.append(cover);
          });
          const covered=await page.evaluate(feedbackLayoutFacts);
          assert.throws(()=>assertFeedbackLayout(covered),error=>error.feedback_layout.actions[0].target.hit===false&&/target is covered/.test(error.message));
          await page.locator('#owned-cover').evaluate(node=>node.remove());
          await check.evaluate(node=>{node.disabled=true});
          const disabled=await page.evaluate(feedbackLayoutFacts);
          assert.equal(disabled.actions[0].target.hit,false);
          assert.equal(assertFeedbackLayout(disabled).compact_right_board_actions,true);
          await page.locator('.play-heading h1').evaluate(node=>{Object.assign(node.style,{width:'14px',wordBreak:'break-all',lineHeight:'.6'})});
          const wrapped=await page.evaluate(feedbackLayoutFacts);
          assert.ok(wrapped.heading.runs.length>1&&wrapped.heading.runs[1][1]<wrapped.heading.runs[0][3],'fixture did not create overlapping wrapped text boxes');
          assert.throws(()=>assertFeedbackLayout(wrapped),/heading wraps/);
          t.diagnostic(JSON.stringify({engine,header_content:facts.header_content,board:facts.board,action_group:proof.action_group,covered_rejected:true,disabled_pointer_transparent_allowed:true}));
        } finally {await context.close()}
      });
      await t.test(engine+' follows the real guide popup, observes its initial document and closes failed visits',async()=>{
        const context=await browser.newContext(contextOptions);
        let status=200,documents=0,webErrors=0,transport=false;
        context.on('response',response=>{if(response.url()==='https://runner.invalid/how-to-play/sudoku'&&response.request().isNavigationRequest())documents++});
        context.on('weberror',()=>webErrors++);
        await context.route('https://runner.invalid/**',route=>transport?route.abort('failed'):route.fulfill({status,contentType:'text/html',body:route.request().url().includes('/how-to-play/')?
          '<main class="guide-page"><h1>How to play Sudoku</h1><section id="controls"><p>Hold a digit and release to pin it.</p><p>Tap the pinned digit to exit. Escape clears pinned entry.</p></section></main><script>throw Error("owned popup fixture")</script>':
          '<main class="sudoku-play"><details class="play-help"><summary>Help · How to play</summary><a href="/how-to-play/sudoku" target="_blank">Full Sudoku guide</a></details></main>'}));
        try {
          const page=await context.newPage();page.setDefaultTimeout(5000);await page.goto('https://runner.invalid/');
          const activate=async target=>{await target.scrollIntoViewIfNeeded();await target.click()};
          const proof=await visitSudokuGuide(page,activate,async guide=>assertSudokuGuide(await guide.evaluate(guideControlFacts)));
          assert.equal(proof.document_status,200);assert.equal(context.pages().length,1);
          await assert.rejects(visitSudokuGuide(page,activate,async()=>{throw Error('owned validation failure')}),/owned validation failure/);
          assert.equal(context.pages().length,1,'guide survived validation-failure cleanup');
          status=503;
          await assert.rejects(visitSudokuGuide(page,activate,async()=>{throw Error('must not inspect a failed document')}),/document did not return success/);
          assert.equal(context.pages().length,1,'guide survived HTTP-failure cleanup');
          status=404;
          await assert.rejects(visitSudokuGuide(page,activate,async()=>{throw Error('must not inspect a failed document')}),/document did not return success/);
          assert.equal(context.pages().length,1,'guide survived 404 cleanup');
          transport=true;
          await assert.rejects(visitSudokuGuide(page,activate,async()=>{throw Error('must not inspect a failed transport')}),/document transport failed/);
          assert.equal(context.pages().length,1,'guide survived transport-failure cleanup');
          assert.equal(documents,4,'initial popup document was missed or observed twice');assert.equal(webErrors,4,'owned popup script error was missed or observed twice');
          t.diagnostic(JSON.stringify({engine,guide:proof.path,document_status:proof.document_status,initial_documents:documents,web_errors:webErrors,failed_validation_closed:true,failed_document_blocked:true,failed_transport_blocked:true}));
        } finally {await context.close()}
      });
    } finally {
      await browser.close();
      t.diagnostic(JSON.stringify({engine,contexts:browser.contexts().length,browser_connected:browser.isConnected()}));
    }
  }
});
