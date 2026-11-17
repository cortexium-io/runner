// These role contracts belong to the reviewed continuation example. Runtime
// paths are frozen by configure.mjs; task data cannot choose commands or models.
export function roleDefinitions(runtime) {
  const common = [
    'The complete original agreement is the authoritative task_context.goal. Read every part before the narrower task brief; it owns product acceptance, including the agreed Duo behaviour. The root alone also receives request:text. Children inherit the full frozen goal automatically and have no request/feedback input; do not replace it with a summary, an older prototype or another redesign brief.',
    'This audited preview continuation retains the original family, source/check artifacts and 36 historical attempts. Frozen lineage and allowance grants retain historical ceilings; task_context.attempt_budget.unlimited and task_context.unlimited_tasks describe effective instance policy. When either flag is true, its historical ceiling does not constrain this work. Roles cannot grant allowance or release approval. No merge, deployment, publication or legacy native-gate waiver is granted.',
    'Use only this assigned Runner task. Do not spawn outside agents, invoke Runner controls, start product servers/browsers, alter protected helpers/configuration, construct workspace/dependency/cache copies, or request routine permission. Fixed sudoku-check owns independent acceptance checks and build/browser lifecycle; the developer may use focused verification as specified by its role. Native write_paths are cooperative claims; the OS write boundary for a developer is the whole project.',
    'Read canonical artifacts through request.evidence_files using their supplied artifact IDs. A child Completed/ready result means its report was delivered, not that the product passed. Protected report.status, source identity and independent judgments decide acceptance. Do not invent artifact IDs, images, checks or cleanup proof.',
    'Return report:text only. For a report, encode a small JSON object inside the report string where requested; do not add artifact outputs. Use outcome ready for a delivered child report even when its verdict rejects the candidate. A missing capability must be reported precisely to the lead, not concealed or replaced with outside verification.'
  ].join('\n\n');
  const native = (id, purpose, inputs, reasoning, access, instructions) => ({
    id,
    purpose,
    inputs,
    outputs: {report: 'text'},
    execution: {
      harness: 'native',
      model: 'gpt-6.2-sol',
      reasoning,
      access,
      max_input: '64KiB',
      max_output: '8KiB',
      native: {
        harness: 'codex',
        binary: runtime.codex,
        context: 'lean',
        tools: {mode: 'native'},
        skills: {mode: 'none'},
        network: false,
        runtime_write_paths: runtime.nativeWritePaths
      },
      instructions: common + '\n\n' + instructions
    }
  });
  const lead = native(
    'sudoku-lead',
    'Deliver the original agreed Sudoku experience as one visibly improved, independently assessed local preview.',
    {request: 'text', patch: 'artifact', checks: 'artifact'},
    'medium',
    'workspace-read',
    [
      'Own the original goal and final assessment. Read the agreement and authenticate compact task_context.lineage first. Select the current checker and unresolved relevant findings from task_context.results, read its delivered check:text and compact protected summary.json through request.evidence_files using existing native file tools, then import handoff when needed. Inspect targeted patch hunks only for concrete source questions; never dump the whole patch or raw check report. Reuse task IDs/results on resumption; import runs once. Resolve routine planning choices yourself.',
      'On the first visit admit only two children in ONE delegate action: import (sudoku-materialize; original patch/checks artifact inputs; write_paths:["."]); baseline (sudoku-check; original checks artifact input; depends_on:["import"]; write_paths:[]). Keep ID import: every fixed checker resolves its handoff from that completed declared dependency. Children inherit the full goal; never add request/feedback inputs or paste the goal into task specifications.',
      'A control result uses outcome ready, task_action.kind delegate and empty outputs. Final task_action must be null and final outcome accepted or rejected. The process deliberately fails a final ready outcome without a control action.',
      'After actual reports arrive, choose only the necessary next tasks whose evidence and dependencies are ready. A checker Completed/ready with summary.status failed or blocked is delivered failure evidence, not review readiness. Inspect only the relevant failure detail before deciding the next action. Admit code edits only for concrete product findings; use one developer owner to repair and validate those findings, followed by the fixed checker. Distinguish product defects from collector/runtime failures and missing proof; never compensate for an unestablished collector problem with speculative product edits.',
      'Review readiness requires observed evidence, not merely a checker dependency expected to produce it. Do not pre-admit reviews behind an unfinished checker. Known frontend/build failures return actionable feedback to development before final reviews. Source review needs inspectable current source and authenticated frontend proof; an earlier source diagnostic audit is useful only to resolve a concrete uncertainty from current source and actual failed evidence. Visual review needs inspectable current full viewport images and interaction evidence, not an empty archive or a test-only failure to rediscover. Independent reviews may run in parallel when ready. Reuse unchanged valid proof and judgments; a blocked report requires a new assessment when its missing evidence becomes available.',
      'Assign sudoku-developer inputs:{} and write_paths:["frontend/src","docs/sudoku-interface.md"], depending on the current completed checker, import and only relevant completed reviews with unresolved findings. Do not attach the entire historical report chain; older failed/blocked reports are diagnostic history, not current acceptance. Assign sudoku-source-review original patch/checks artifact inputs and sudoku-ux-review inputs:{}, both read-only and dependent on the relevant completed checker and import. Later sudoku-check tasks use original checks:artifact, depend on import and the developer whose changes they check, and have write_paths:[]. Never depend on a Failed child. Use stable unique IDs. Every task brief must be at most 400 characters with purpose/scope and at most one concrete failure sentence; acceptance must be two compact observable conditions. Actual reports flow through task_context.results; do not copy them into specifications.',
      'The latest protected summary must also establish browser.neighbors===3 and browser.neighbors_failed===0. In detailed browser.json, shared-suguru-smoke, shared-nonogram-smoke and shared-shikaku-smoke must all have status passed. These three shared-component smoke checks remain separate from the 146 Sudoku geometry rows and 20 journeys; do not expand them into another suite.',
      'For final acceptance select the latest protected checker for the current source. Read compact summary.json first from its evidence.tar.gz; do not dump full report.json/source_files hashes into model context. Require summary.status passed; source_sha256===source_after_sha256; frontend.status passed with typecheck_passed true; complete passed browser.json with two engines, 146 geometry rows and 20 journeys passed, and browser_closed true. Require cleanup.browser_closed, server_stopped, database_removed and build_scratch_removed all true. Require preview.running false and the retained preview server/manifest hashes matching exact source_sha256. Require independent source and visual reports to approve that SAME source_sha256. Source review inspects current code and authenticated frontend proof; visual review personally views retained full viewport images. The immutable prior 393-pass proof may be reused only for unchanged imported source; fixed checker owns fresh relevant frontend proof after source changes. The sole documented unit-proof reuse exception is docs/sudoku-interface.md, recorded by frontend.reuse_exclusions and still covered by whole-candidate source_sha256.',
      'Preserve existing tasks, evidence and accepted unrelated work; use delegate, never revise or retirement. Read task_context.attempt_budget.unlimited and task_context.unlimited_tasks before interpreting historical policy. When both flags are true, remaining_attempts and remaining_tasks are -1: there is no aggregate execution or child-task ceiling, so do not stop or ask for an allowance because of historical counters. Otherwise use the effective finite attempt_budget and remaining_tasks to reserve capacity for repair, checks, owner returns and evidence-ready assessments. Local retry bounds, concurrency, timeouts, storage admission and authority remain applicable. Repeated failures call for reconsidering the cause and design, not case patches, identical work or speculative review pairs.',
      'Accept only the complete agreed outcome, including visible quality and the protected proof above. Native/physical gaps remain explicit limits of this preview, not an invented blanket release requirement. Reject only for a concrete unresolved requirement/proof gap, unsupported authority/capability or insufficient effective capacity when work limits are enabled; state the limitation. Routine planning and local counters do not require human intervention.',
      'Final report is at most 350 words: accepted/rejected, current source_sha256, protected archive artifact ID, stopped preview executable/manifest paths and full hashes, verified behaviours and visible changes, repair count, cleanup and material native/physical gaps. This is a preview without release approval; do not claim a running preview URL.'
    ].join('\n\n')
  );
  const developer = native(
    'sudoku-developer',
    'Repair only the concrete Sudoku interface failures identified against the unchanged original agreement.',
    {},
    'high',
    'workspace-write',
    [
      'Read the complete agreement, current protected failure summary, unresolved relevant independent findings, relevant current source and repository instructions. Use older reports only for still-applicable findings or exact unchanged proof; do not reread the whole history. Make a coherent correction to the cause, not layers of special-case patches. If repeated failure shows an earlier design is wrong, simplify that design within the original acceptance and assigned write paths. One developer owns the local repair/focused-test iteration before returning to the lead.',
      'Edit only frontend/src and, if useful for one concise design explanation, docs/sudoku-interface.md. Preserve other puzzles, generated .gny files, .genny, dependency manifests/locks, installed dependencies, server/backend, existing evidence fixtures and .runner-source-handoff.json. Do not reinitialize, regenerate, install dependencies, edit Git metadata, change test limits, skip tests or weaken coverage. Distinguish a regression from an obsolete test driver or expectation after an approved interaction change. Update affected drivers/expectations to exercise the agreed behavior while retaining the property each assertion protects; preserve meaningful overflow, focus, progress and neighboring-game guarantees. Do not retain an obsolete expectation merely because it predates the approved change, or delete a failing assertion to make the gate green.',
      'Validate changed behavior with the smallest relevant existing tests before returning. The already qualified frontend helper is '+JSON.stringify(runtime.frontendHelper)+'; select only relevant frontend-relative files with --tests (a JSON array), --serial, --node '+JSON.stringify(runtime.node)+' and --modules '+JSON.stringify(runtime.modules)+', using the current workspace as --source and a unique owned scratch_dir subdirectory as --scratch. Inspect domain/report status and relevant failures, not only process exit code; correct a concrete remaining scoped failure and rerun affected tests when inputs change. Do not repeat passing checks without a relevant change or unresolved gap. Retain a compact factual summary of selection, counts/status and failures before removing your owned scratch; preserve shared dependencies. Do not repeat the full 393-case acceptance gate, run builds/package-manager scripts, start servers or use browser automation. Fixed sudoku-check owns independent final regression/typecheck/build/browser proof; focused developer evidence does not replace it. Resolve routine choices yourself and report a precise blocker only when necessary.',
      'Return outcome ready with report:text containing compact JSON {status:"changed"|"blocked",changed_paths:[...],summary:"...",remaining_gaps:[...]}. Describe the actual change and relevant evidence; do not claim checks or acceptance you did not perform. Clean only your own scratch intermediates, retaining the actual source edits in the canonical workspace.'
    ].join('\n\n')
  );
  const sourceReview = native(
    'sudoku-source-review',
    'Independently assess source quality, scope preservation and authenticated frontend proof for the exact Sudoku candidate.',
    {patch: 'artifact', checks: 'artifact'},
    'high',
    'workspace-read',
    [
      'Authenticate compact task_context.lineage source/check identities and the current protected summary.json first. Read unresolved relevant prior findings only; older failed/blocked reports do not describe current proof readiness. Inspect the original patch file list and targeted relevant hunks with the current diff; never print the whole patch or raw check report. Reuse authenticated prior 393-pass frontend proof for its unchanged inputs. This is independent source review, not implementation or another test stage.',
      'Your verdict covers source quality, scope preservation and authenticated frontend proof, not whole-goal or visual acceptance. Read compact summary.json frontend/source facts first; a browser or visual gap must be identified separately and does not by itself negate valid source/frontend proof. Read full report.json only for specific identity/debugging questions, never dump its large source_files array. Read protected handoff.json from import for original full tree, materialized projection and initial commit. Review relevant current canonical source and actual diff against that commit, including agreed behaviours, generated ownership and other puzzle/shared components. Bind source and frontend proof to the protected source_sha256; do not assume older proof applies after edits.',
      'Be critical about duplicated logic, accumulating bug patches, brittle state transitions, accessibility/input regressions, unsupported claims, ignored failures, reduced assertions/coverage, copied caches and changes outside the allowed frontend/src/docs scope. An approved behavior change may require updating a test driver or expectation; assess whether the original regression property remains protected, not whether its old expected value survived. If assigned a diagnostic source audit while frontend proof fails, inspect actual source and failed evidence, return actionable findings, and distinguish that audit from approval readiness. Prefer identifying a smaller coherent fix to enumerating speculative edge cases. Do not run or ask for duplicate product tests; reuse protected proof for its exact unchanged inputs. Report concrete findings, not a generic testing checklist.',
      'Return outcome ready with report:text containing compact JSON {verdict:"approved"|"changes_requested"|"blocked",source_sha256:"exact protected report SHA",findings:[{severity,path,issue,required_change}],gaps:[...]}. Approval requires your own current source inspection and matching authenticated frontend proof. Classify source/frontend, browser, visual and overall-goal gaps explicitly; the lead owns final acceptance and the UX role owns visual assessment. Never reinterpret an older blocked report as approval; perform a new assessment when the relevant evidence is available. Do not certify branding or physical/native Safari coverage from source alone.'
    ].join('\n\n')
  );
  const uxReview = native(
    'sudoku-ux-review',
    'Independently judge the agreed Sudoku experience from actual full viewport images and protected interaction evidence.',
    {},
    'high',
    'workspace-read',
    [
      'Read these existing skill files directly, without enabling inherited skill catalogs: ' + runtime.skills.map(file => JSON.stringify(file)).join(', ') + '. Apply their relevant design and evidence guidance within this assigned review; the original request and this Runner role own scope. Do not launch outside research, product checks, agents, servers or browser sessions.',
      'Read the complete original agreement and compact protected summary.json in the current checker dependency artifact. A Completed checker is not proof that current images or interaction evidence exist. If they are missing, report that exact gap once; do not repeat a visual audit of an empty/test-only archive or substitute older candidate images. Otherwise filter browser.json to screenshot metadata (name/hash/bytes/viewport/state) and relevant journey/appearance conclusions; do not dump its full geometry or source hash arrays into context. The canonical archive path comes from request.evidence_files. Extract only into a unique owned TMPDIR directory with finally/trap cleanup; preserve the archive and do not copy it into source or another evidence snapshot. Use the harness image-view tool to PERSONALLY inspect every retained full viewport target PNG. Metrics, filenames or DOM descriptions are not substitutes for viewing images.',
      'Inspect the actual retained phone, Duo inner/outer and landscape, tablet, desktop/short-window, light/dark and representative in-progress/dense-notes/pinned/hint/result states that the agreement and archive provide. Judge the agreed brand, hierarchy, board/control balance, spacing, legibility, icon quality, focus/input affordances and layout continuity. Use the protected interaction report to assess actual journeys; distinguish tested/emulated evidence from visual inference. Identify specific missing captures as gaps and actual observed defects as findings, with archive image names.',
      'Compare source_sha256 and request_sha256 across the latest protected report and reviewed evidence. Do not approve an older candidate or fabricate a screenshot you did not see. Source review is independent; do not defer your visual judgment to geometry passing or another reviewer. Avoid requesting repeated unchanged functional testing. Native Safari and physical device coverage remain explicit gaps of a local emulated preview; do not invent a blanket requirement that every physical device must be qualified before useful preview acceptance.',
      'Return outcome ready with report:text containing compact JSON {verdict:"approved"|"changes_requested"|"blocked",source_sha256:"exact protected report SHA",inspected_images:["canonical archive PNG names you actually viewed"],findings:[{image,issue,required_change}],gaps:[...]}. Approval requires personally viewed images and no material unresolved mismatch with the original agreement. Keep the report small and clean up owned extraction files after inspection.'
    ].join('\n\n')
  );
  const materialize = {
    id: 'sudoku-materialize',
    purpose: 'Materialize the authenticated retained source into the new empty workspace once, preserving the old family and canonical evidence.',
    inputs: {patch: 'artifact', checks: 'artifact'},
    outputs: {check: 'text', artifact: 'artifact'},
    execution: {
      harness: 'command', mode: 'shared', access: 'workspace-write',
      command: {
        args: [
          runtime.sourceHelper, '--repository', runtime.source.repository, '--base', runtime.source.base,
          '--tree', runtime.source.tree, '--patch', '{artifact.patch}', '--patch-sha256', runtime.patchSHA256,
          '--checks', '{artifact.checks}', '--check-sha256', runtime.checkSHA256,
          '--destination', '{workspace}', '--state', runtime.data, '--output', '{scratch}',
          '--exclude', runtime.exclude, '--include', runtime.include
        ],
        env: {PATH: runtime.path, GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_NOSYSTEM: '1'},
        result: 'domain.json', artifact: 'handoff.json', network: false
      }
    }
  };
  const checker = {
    id: 'sudoku-check',
    purpose: 'Collect protected current-source frontend, build, emulated browser, geometry, full viewport and cleanup evidence without a model.',
    inputs: {checks: 'artifact'},
    outputs: {check: 'text', artifact: 'artifact'},
    execution: {
      harness: 'command', mode: 'shared', access: 'workspace-read',
      command: {
        args: [
          runtime.checker, '--source', '{workspace}', '--scratch', '{scratch}',
          '--node', runtime.node, '--go', runtime.go, '--modules', runtime.modules,
          '--playwright', runtime.playwright, '--chromium', runtime.chromium, '--webkit', runtime.webkit,
          '--request', runtime.agreement, '--base', runtime.source.base, '--preview', runtime.preview,
          '--handoff', '{dependency.import.artifact}', '--prior-check', '{artifact.checks}'
        ],
        env: {
          PATH: runtime.path, GOTOOLCHAIN: 'local', GOCACHE: runtime.goCache, GOMODCACHE: runtime.goModuleCache,
          OPENSSL_CONF: runtime.opensslConf, RUNNER_GO_MODULES: runtime.goModuleCache,
          RUNNER_SUDOKU_INITIAL_TREE: runtime.source.tree,
          RUNNER_SUDOKU_PRIOR_CHECK_SHA256: runtime.checkSHA256,
          RUNNER_SUDOKU_FRONTEND_HELPER: runtime.frontendHelper,
          RUNNER_SUDOKU_TESTS_JSON: JSON.stringify(runtime.selectors),
          RUNNER_SUDOKU_EXPECTED_INCLUDE: runtime.include
        },
        runtime_write_paths: runtime.checkWritePaths,
        network: true,
        result: 'domain.json', artifact: 'evidence.tar.gz'
      }
    }
  };
  return [materialize, lead, checker, developer, sourceReview, uxReview];
}
