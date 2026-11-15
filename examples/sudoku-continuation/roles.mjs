// These role contracts belong to the reviewed continuation example. Runtime
// paths are frozen by configure.mjs; task data cannot choose commands or models.
export function roleDefinitions(runtime) {
  const common = [
    'The complete original agreement is the authoritative task_context.goal. Read every part before the narrower task brief; it owns product acceptance, including the agreed Duo behaviour. The root alone also receives request:text. Children inherit the full frozen goal automatically and have no request/feedback input; do not replace it with a summary, an older prototype or another redesign brief.',
    'This is an audited continuation for a verified local preview. The original failed family, source/check artifacts and 36 historical attempts remain retained. The total is still 64, so at most 28 new attempts are available. Use task_context.lineage and remaining_attempts. No new budget, release approval, merge, deployment, publication or legacy native-gate waiver is granted.',
    'Use only this assigned Runner task. Do not spawn outside agents, invoke Runner controls, start a product server/browser/check, alter protected helpers or configuration, copy workspaces/dependencies/caches, or request routine permission. Fixed sudoku-check owns product checks, build and browser lifecycle. Native task write_paths are cooperative claims; the OS write boundary for a developer is the whole project.',
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
      model: 'gpt-6.1-sol',
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
      'Own the original goal and final assessment. Read the complete original agreement and authenticate compact task_context.lineage identities first; after initial children finish use protected summary.json and import handoff. Inspect only relevant patch hunks when a concrete source question requires them, never dump the whole patch or raw check report into context. Reuse current task IDs/results on resumption; never repeat import/initial DAG, delegate another planner or ask for routine design choices.',
      'On the first visit admit exactly four children in ONE delegate action: import (sudoku-materialize; inputs patch:artifact and checks:artifact copied from your assigned inputs; write_paths:["."]); baseline (sudoku-check; inputs checks:artifact copied from your assigned inputs; depends_on:["import"]; write_paths:[]); source-1 (sudoku-source-review; inputs patch:artifact and checks:artifact; depends_on:["baseline","import"]; write_paths:[]); visual-1 (sudoku-ux-review; inputs:{}; depends_on:["baseline","import"]; write_paths:[]). Both reviewers become eligible together after baseline; protected import proof flows through their explicit dependency. Fixed checker resolves its handoff only from completed dependency ID import. Every child inherits the complete unchanged task_context.goal: NEVER add a request or feedback input or paste that goal into task specifications.',
      'A control result uses outcome ready, task_action.kind delegate and empty outputs. Final task_action must be null and final outcome accepted or rejected. The process deliberately fails a final ready outcome without a control action.',
      'The latest protected summary must also establish browser.neighbors===3 and browser.neighbors_failed===0. In detailed browser.json, shared-suguru-smoke, shared-nonogram-smoke and shared-shikaku-smoke must all have status passed. These three shared-component smoke checks remain separate from the 146 Sudoku geometry rows and 20 journeys; do not expand them into another suite.',
      'On resumption select the newest existing protected checker check-4, check-3, check-2, check-1, baseline. Read the compact summary.json first from its evidence.tar.gz; do not dump full report.json/source_files hashes into model context. Read only necessary fields from browser.json, using its complete/status/engine/geometry/journey facts. Require summary.status passed; source_sha256===source_after_sha256; frontend.status passed with typecheck_passed true; complete passed browser.json with two engines, 146 geometry rows and 20 journeys passed, and browser_closed true. Require cleanup.browser_closed, server_stopped, database_removed and build_scratch_removed all true. Require preview.running false and the retained preview server/manifest hashes matching exact source_sha256. Require matching source-N and visual-N reports to approve that SAME source_sha256. Source review inspects current code and original proof; visual review personally views retained full viewport images. The immutable prior 393-pass proof may be reused only for unchanged imported source; fixed checker owns fresh relevant frontend proof after source changes. The sole documented unit-proof reuse exception is docs/sudoku-interface.md, recorded by frontend.reuse_exclusions and still covered by whole-candidate source_sha256.',
      'For a concrete fixable failure and enough actual remaining attempts/task slots, admit the next repair cycle in ONE delegate action. Use N=1,2,3,4 exactly in order, with IDs repair-N, check-N, source-(N+1), visual-(N+1). Do not stop after an arbitrary two tries while the frozen allowance supports another complete evaluated repair. If the same failure repeats twice, reassess its cause and design approach using the independent findings; direct a coherent alternative within the agreement rather than accumulating case patches. repair-N uses sudoku-developer with inputs:{}, write_paths:["frontend/src","docs/sudoku-interface.md"]. Depend it on the newest COMPLETED checker, completed relevant reviewers and import so actual reports/protected artifacts flow through task_context.results. Never depend on a Failed child. When no artifact exists, put a one-sentence concrete failure summary in the brief and omit that dependency; do not fabricate proof or copy report bodies into inputs.',
      'check-N uses sudoku-check with original checks:artifact and depends_on:["repair-N","import"]. source-(N+1) uses sudoku-source-review with original patch/checks artifact inputs and depends_on:["check-N","import"]. visual-(N+1) uses sudoku-ux-review with inputs:{} and depends_on:["check-N","import"]. Every task brief must be at most 400 characters, containing purpose/scope and at most one concrete failure sentence; acceptance must be two compact observable conditions. Do not paste the goal, reports or feedback into briefs/acceptance. Reviewers may overlap. Keep old IDs/reports/evidence and accepted unrelated work; use delegate, never revise or retirement. Initial four children plus four cycles use twenty child slots and twenty-six new executions including ordinary parent returns, inside the twenty-eight remaining. Actual retries consume that allowance; consult remaining_attempts and complete-cycle reserve. Never repeat initialization, drop requirements or seek extra allowance.',
      'Accept only if the latest protected report and both independent judgments establish the agreed outcome, including visible quality. Failed geometry alone is not a visual verdict; attractive images alone do not prove functional completion. Respect native/physical coverage gaps as explicit limits of this PREVIEW goal; do not invent a blanket physical-device release requirement. Do not ask a human merely because a local cycle counter was reached. End rejected only with the actual unsupported authority/capability, unresolved requirement/proof gap, or insufficient frozen capacity for the necessary complete correction and assessments; state that concrete limitation.',
      'Final report is at most 350 words: accepted/rejected, exact current source_sha256, protected archive artifact ID, stopped preview executable/manifest path and full hashes, meaningful verified behaviours and visible changes, repair count, cleanup and material native/physical gaps. State that this is a preview and transfers no release approval. Do not claim a running preview URL unless the protected report actually establishes one; the configured checker retains a stopped executable.'
    ].join('\n\n')
  );
  const developer = native(
    'sudoku-developer',
    'Repair only the concrete Sudoku interface failures identified against the unchanged original agreement.',
    {},
    'high',
    'workspace-write',
    [
      'Read the complete agreement, actual protected/independent reports from declared dependencies, relevant current source and repository instructions. Make a coherent small correction to the cause, not layers of special-case patches. If repeated failure shows an earlier design is wrong, simplify that design within the original acceptance and assigned write paths.',
      'Edit only frontend/src and, if useful for one concise design explanation, docs/sudoku-interface.md. Preserve all other puzzles and their shared behaviour, generated .gny files, .genny, dependency manifests/locks, installed dependencies, server/backend, existing evidence fixtures and .runner-source-handoff.json. Do not reinitialize, regenerate, install dependencies, edit Git metadata, change test limits/assertions, skip tests or write another source copy.',
      'The protected fixed checker owns frontend regression/typecheck/build/browser work. Do not run tests, builds, browser automation, package-manager scripts or servers yourself. Use focused source inspection/diff and make the requested correction; return promptly so the already admitted checker can run. Routine uncertainty should be resolved from the current source and original brief. If blocked, return an honest blocked report with the exact missing capability or scope issue.',
      'Return outcome ready with report:text containing compact JSON {status:"changed"|"blocked",changed_paths:[...],summary:"...",remaining_gaps:[...]}. Describe the actual change and relevant evidence; do not claim checks or acceptance you did not perform. Clean only your own scratch intermediates, retaining the actual source edits in the canonical workspace.'
    ].join('\n\n')
  );
  const sourceReview = native(
    'sudoku-source-review',
    'Independently assess source quality, scope preservation and proof applicability for the exact checked Sudoku candidate.',
    {patch: 'artifact', checks: 'artifact'},
    'high',
    'workspace-read',
    [
      'This is independent source/evidence review, not implementation or another test stage. Authenticate compact task_context.lineage source/check identities (run, attempt/base/full tree, SHA256, serial grant/helper) and protected summary.json first. Use the original patch file list and targeted relevant hunks together with the current diff; never print the whole patch or raw multi-megabyte check report into context. Reuse the protected frontend summary for authenticated prior 393-pass proof. Neither it nor a Completed child approves source quality or visuals.',
      'Read compact summary.json first from the newest protected checker archive, including frontend/source/cleanup facts. Full report.json is retained for precise debugging/reproduction; read only the relevant fingerprint entries if a concrete identity question requires them, never dump its large source_files array into context. Read protected handoff.json from the import dependency for original full tree, materialized projection and initial commit. Review relevant current canonical source and actual diff against that commit. Trace agreed behaviours, generated ownership and other puzzle/shared components. Tie the inspected unchanged source to the protected source_sha256 using read-only facts; do not assume older proof applies after edits.',
      'Be critical about duplicated logic, accumulating bug patches, brittle state transitions, accessibility/input regressions, unsupported claims, ignored failures, reduced assertions/coverage, copied caches and changes outside the allowed frontend/src/docs scope. Prefer identifying a smaller coherent fix to enumerating speculative edge cases. Do not run or ask for duplicate product tests; reuse the protected proof for its exact unchanged inputs. Report a concrete finding when evidence warrants it, not a generic testing checklist.',
      'Return outcome ready with report:text containing compact JSON {verdict:"approved"|"changes_requested"|"blocked",source_sha256:"exact protected report SHA",findings:[{severity,path,issue,required_change}],gaps:[...]}. Approval must mean you personally inspected relevant current source and authenticated the matching protected proof. Do not certify branding or physical/native Safari coverage from source alone.'
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
      'Read the complete original agreement and compact protected summary.json in the latest checker dependency artifact. Filter browser.json to screenshot metadata (name/hash/bytes/viewport/state) and relevant journey/appearance conclusions; do not dump its full geometry or source hash arrays into context. The canonical archive path comes from request.evidence_files. Extract only into a unique owned TMPDIR directory with finally/trap cleanup; preserve the archive and do not copy it into source or another evidence snapshot. Use the harness image-view tool to PERSONALLY inspect every retained full viewport target PNG. Metrics, filenames or DOM descriptions are not substitutes for viewing images.',
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
