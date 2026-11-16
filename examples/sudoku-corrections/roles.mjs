import {followupFacts} from '../sudoku-continuation/followup.mjs';

export const processID='correct-sudoku-preview';
export const goalStageID='stage-sudoku-goal';
export const goalStageRoleID='sudoku-correction-goal-stage';
export const testEntry='frontend/verification/sudoku-corrections.mjs';
export const testHelpers='frontend/verification/sudoku-corrections';
export const names=Object.freeze({lead:'sudoku-correction-lead',baseline:'sudoku-correction-baseline',research:'sudoku-correction-research',
  author:'sudoku-correction-test-author',developer:'sudoku-correction-developer',check:'sudoku-correction-check',
  source:'sudoku-correction-source-review',visual:'sudoku-correction-ux-review'});

export function correctionFacts(parent) {
  if(!parent.roles?.[names.check])return followupFacts(parent);
  if(!parent.id||parent.status!=='Completed'||!parent.resources_released||parent.cancel_requested||parent.cleanup_error||parent.cleanup_blocked||
      !Number.isSafeInteger(parent.revision)||parent.revision<1||parent.task_graph?.root_run_id!==parent.id||parent.task_graph.parent_run_id||
      !Array.isArray(parent.attempts)||parent.attempts.some(a=>a.state!=='finished'||!a.released)||!parent.process?.tasks||
      typeof parent.repository!=='string'||!parent.repository.startsWith('/'))throw new Error('correction follow-up needs the completed settled accepted root');
  const nativeRoles=Object.fromEntries(['lead','developer','source','visual'].map(kind=>[kind,names[kind]]));
  for(const id of Object.values(nativeRoles)) {
    const p=parent.roles[id]?.execution;
    if(p?.harness!=='native'||!p.native||!p.model||!p.instructions)throw new Error('accepted correction native profile missing: '+id);
  }
  const checker=parent.roles[names.check].execution;
  if(checker?.harness!=='command'||checker.mode!=='shared'||checker.access!=='workspace-read'||!Array.isArray(checker.command?.args)||
      !checker.command.env?.RUNNER_SUDOKU_ADMISSION)throw new Error('accepted correction fixed runtime is missing');
  const patch=parent.inputs?.patch?.artifact_id,checks=parent.inputs?.checks?.artifact_id;
  if(![patch,checks].every(id=>typeof id==='string'&&/^[A-Za-z0-9_-]+\/[A-Za-z0-9_.-]+$/.test(id)))throw new Error('accepted correction root lacks original artifact inputs');
  return {patch,checks,checker,nativeRoles};
}

const common=[
  'The unchanged owner_goal:artifact is the complete authoritative owner document. task_context.goal is only its compact digest/purpose pointer, NOT the acceptance specification. Resolve your declared owner_goal through request.evidence_files, verify its SHA256 against that pointer, then read the overall obligations and relevant full sections/captures. Iterate actual corrections[] IDs; no requirement is truncated. Never copy the full document into briefs/inputs/reports. New explicit corrections supersede historical acceptance; prior proof establishes starting source, not current approval. Preserve source/progress/history.',
  'Work only in this Runner task. No outside agents/Runner controls, protected-helper/config changes, installs, generated-source edits, credentials, merge/deploy/restore or gate waivers. Respect narrow cooperative write_paths; report a real unsupported capability instead of inventing proof or changing policy.',
  'Use only supplied canonical artifacts and declared dependency outputs. Read small summary/report sections first, not whole inventories/history. Keep final report:text <=1024 UTF-8 bytes and reason<=256 bytes; the whole final response remains within its existing8192-byte bound. Full citations/per-ID/image reconciliation belong in declared artifact files under request.output_dir, returned as relative filenames; never inline those files in report. Completed/ready delivers evidence, not acceptance. Clean owned scratch in finally/trap; retain edits and canonical artifacts.',
  'Effective task_context.attempt_budget.unlimited/task_context.unlimited_tasks with remaining -1 disable aggregate ceilings; historical counters never require allowance. Local retries/access/concurrency/timeouts/storage/authority still apply. A delivered report uses outcome ready even when its verdict is changes_requested or blocked.'
].join('\n\n');

export function correctionRoles(parent,runtime) {
  const {nativeRoles}=correctionFacts(parent);
  const native=(kind,id,purpose,instructions)=>{
    const role=structuredClone(parent.roles[nativeRoles[kind]]);
    Object.assign(role,{id,purpose,inputs:{owner_goal:'artifact'},outputs:{report:'text'}});
    role.execution.instructions=common+'\n\n'+instructions.join('\n\n');
    return role;
  };
  const lead=native('lead',names.lead,'Deliver every current owner correction through independent current-source evidence and an accepted stopped preview.',[
    'Every task receives owner_goal copied UNCHANGED from your declared artifact input, including baseline/research/writers/check/reviews/repairs. First delegate baseline, role '+names.baseline+', inputs owner_goal/prior/handoff, write_paths:[]. Require its archive manifest.status passed before writers; Completed alone is insufficient. The exact original owner document binds source admission; the compact pointer must never replace --request or --goal. No repeated baseline build or legacy UI gate is needed.',
    'Current C13 requires official sharing research ('+names.research+', ID sharing-research, depends_on:["baseline"]) before both writers: admit research plus developer '+names.developer+' and test author '+names.author+' together; BOTH writers depend_on:["baseline","sharing-research"]. For later goals require this research only when requested sharing changes need fresh official evidence; otherwise reuse applicable research or omit irrelevant research and both writers depend on baseline. Author owns only '+testEntry+' and '+testHelpers+'; developer owns frontend/src and docs/sudoku-interface.md. Disjoint writers may overlap. Developer may run focused tests; author never changes product. Neither directly launches product browsers/servers.',
    'The fixed '+names.check+' role executes the authored script under qualified command/shared read-only access. Admit it only after baseline, current author and current developer finish; dependency list MUST include baseline and all writers whose bytes it checks. Inputs checks is original retained frontend proof; prior is the newest applicable canonical evidence archive (parent archive initially, current completed check afterward). Keep baseline ID stable for {dependency.baseline.artifact}. Do not treat a collector defect as a product defect or speculate repairs before reading actual diagnostic evidence.',
    'A missing/incomplete archive, startup/import/CLI error or Failed fixed-command child is a verifier failure to diagnose, not a reason to wait for nonexistent summary.json or call an operator. Assign the project test author a compact actual failure and current source/baseline; omit unavailable/Failed dependencies rather than inventing artifacts. The repaired fixed check depends on the current completed author, baseline and unchanged relevant product writer. Exit code0/ready/Completed alone never means acceptance. A genuine missing runtime/authority is reported precisely; ordinary project-script faults are repaired inside this process.',
    'Assess every actual goal.corrections[] ID from current source/test/runtime/goal-bound results, meaningful interactions and whole/open-panel images. Require all listed items passed, source_sha256===source_after_sha256, test SHA, runtime hashes, frontend/typecheck/build proof, admission compatibility and cleanup, with no unrun requirement. No old tool, hint or case count overrides this goal. Reuse only exact relevant-input/contract proof; verification-only changes do not invalidate unchanged product frontend/build proof.',
    'Only after current evidence is ready admit independent source/UX reviews ('+names.source+' and '+names.visual+') in parallel behind baseline+completed check. Read their canonical assessment artifacts, not just compact verdicts; require EVERY owner correction ID and SAME source/test/goal identities. Read research artifact for full dated official claims before writers rely on them. UX PERSONALLY views images. Route product findings to developer and oracle faults to author; reuse valid proof and preserve history.',
    'Briefs stay <=400 characters with two observable acceptance conditions; results flow through task_context.results. Control: outcome ready, task_action.kind delegate, empty outputs. Final: task_action null, outcome accepted/rejected. Accept the complete current goal, exact stopped preview manifest/server, independent matching approvals and cleanup. Return the authorized preview handoff; never launch a daemon or claim phone reachability. Preserve existing preview/progress until trusted operator validates replacement. Stop for Dan inspection, no release authority.',
    'Final report <=1024 bytes: verdict, owner-goal/source identities, canonical check and assessment artifact IDs, stopped preview manifest reference, concise visible changes, cleanup and real coverage gaps. Complete per-ID proof remains in those artifacts. Routine decisions/disabled counters need no intervention; actual unsupported capability remains explicit.'
  ]);
  lead.inputs={request:'text',owner_goal:'artifact',patch:'artifact',checks:'artifact',prior:'artifact',handoff:'artifact'};
  const research=native('source',names.research,'Research official sharing endpoints/constraints required by the current goal, or establish no new research is needed.',[
    'If current corrections request sharing changes, use permitted network tools/HTTP for current official sharing endpoints, encoding, native share/clipboard and cancellation/fallback constraints. Cite date, exact first-party URLs and supported claims. Do not invent endpoints. If no such change is requested, return not-needed with the applicable retained research reference; do not perform irrelevant research. Never post, sign up, log in, invoke sharing externally or expose private progress. This is read-only research, not product testing.',
    'Write complete sharing-research.json in request.output_dir: {status,owner_goal_sha256,observed_at,sources:[{url,supports}],platforms:[{name,endpoint,parameters,constraints}],native_share:{availability,cancellation,fallback},gaps}. Preserve all dated first-party citations/constraints in that file, not the final response. Return outputs.research="sharing-research.json" and report:text <=1024 bytes with status/date/covered topics/gaps only. Production https://jacksbreak.com links contain public puzzle identity, never private entries/notes/timer/mistakes. Writers read your canonical artifact.'
  ]);
  research.outputs.research='artifact';
  research.execution.native.network=true;
  const developer=native('developer',names.developer,'Implement current scoped Sudoku corrections with relevant focused regression evidence.',[
    'Wait for authenticated baseline. Read every actual goal.corrections[] item, current source and applicable sharing research. Own only frontend/src and docs/sudoku-interface.md. Preserve backend, generated .gny/.genny, manifests/locks, dependencies, fixtures, Git metadata, accepted preview/progress and other games. The independent author owns '+testEntry+' and helpers; do not edit them.',
    'Correct causes coherently and simplify an earlier design when repeated failures justify it. Implement requested controls, hint choices, chooser, sharing/print and composition/state semantics from the goal; do not retain obsolete earlier gates or replace them with cosmetic/fake behavior. Reason about state, keyboard/accessibility, cancellation, notes/history/resume and relevant shared neighbors.',
    'Run the smallest relevant existing frontend tests through the qualified helper '+JSON.stringify(runtime.frontendHelper)+' with --tests (JSON array), --serial, --node '+JSON.stringify(runtime.node)+' --modules '+JSON.stringify(runtime.modules)+', current workspace --source and a unique owned scratch_dir --scratch. Read actual domain/report status, fix concrete scoped failures and repeat only changed relevant inputs. Legitimate driver/expectation updates must preserve the protected regression property. No full acceptance repetition, browser/server/build launch, dependency installation or test weakening; separate fixed executor owns final proof.',
    'Return compact JSON {status:"changed"|"blocked",changed_paths:[...],summary:"...",focused_checks:[{selection,status,count,reason}],remaining_gaps:[...]}. Preserve actual source edits and clean owned scratch. Independent evidence and source/UX judgment, not this report, establish readiness.'
  ]);
  const author=native('developer',names.author,'Author/repair meaningful project verification of current correction IDs without changing product implementation.',[
    'Wait for baseline, inspect actual source/UI contracts and the complete request. Own only '+testEntry+' and helper files under '+testHelpers+'/. Do not edit application source, protected helpers/config, historical evidence or dependency/runtime settings. Author the runner INSIDE this assigned task; it is intentionally absent at intake. Report actual contract impossibility instead of patching product or accepting a missing check.',
    'Fixed CLI: Node '+testEntry+' --source ABS --scratch ABS --goal ABS --baseline ABS --prior ABS --checks ABS. Configured pins: RUNNER_SUDOKU_GO/MODULES/PLAYWRIGHT/CHROMIUM/WEBKIT/FRONTEND_HELPER/ADMISSION/PREVIEW_ROOT. Read immutable goal/baseline and relevant canonical prior evidence. Preserve normal HOME/account/shared Go caches. No installation, worktree, source mutation, personal browser, posting/signup or TLS/security/engine-guard waiver.',
    'The authored verification implementation must be self-contained within its project-owned surface plus installed pinned runtimes and the fixed frontend helper. Runner helper source may explain an existing build/input/cleanup contract; never import or execute mutable caller-side source files as part of project verification. Handle CLI/import/startup errors with useful bounded failure evidence where possible; a malformed early entry still returns to you through the lead for repair, not an external protected-helper patch.',
    'This project driver later runs separately under command/shared read-only execution. Use real public UI/API journeys, trusted input and viewport/hit/actionability facts; no injected app state, solver substitution, fake acceptance or force clicks. Focused controlled fixtures may test unavailable/cancel platform boundaries (e.g. share AbortError); pair with real link/copy/fallback UI proof and label emulated boundary evidence, never actual OS share success or physical coverage. Iterate actual goal.corrections[] IDs; missing/unrun items block acceptance. Separate function/geometry from visual judgment. Use proportionate requested viewport/theme/state coverage and whole/open-panel images, no blind Cartesian expansion or superseded counts. Check neighbors only where shared inputs changed.',
    'Own build/server/database/browser scratch inside --scratch; source and shared dependencies remain read-only. Reuse installed modules via an OWNED node_modules directory linking immutable packages and excluding shared caches; tooling caches stay private. Match owning package build commands, use cwd-relative generated Node entrypoints, and verify generated declared server document outputs before Go embedding. Reuse valid frontend/typecheck/build proof only when exact relevant product/runtime/lock/input hashes match; changing only verification files must not force unchanged product tests/build. Retain one immutable source-bound executable when valid reuse is needed, not repeated copies. Authenticate prior declared retained preview ownership/hash before reuse; source/test/goal/verification contract changes still require applicable interaction proof.',
    'Finally close owned browsers/contexts, stop/wait server, remove DB/build/dependency-wrapper/temp on success AND failure. Bound diagnostics/images while writing. domain.json contains EXACTLY outcome and reason; rich fields belong in report.json/summary.json. ready delivers evidence, never acceptance. Preserve useful failures/unrun reasons. No transcripts, credentials/private progress, source snapshots, caches/dependencies or server binary in evidence. Bind current source/test/goal/baseline/prior/runtime versions/hashes and numbered results; summary is small. Metrics do not approve visual quality.',
    'Make passing evidence reusable by the unchanged RUNNER_SUDOKU_ADMISSION --mode admit-followup contract. Canonical report.json schema:1,status:passed,base=unchanged handoff.source.base (original upstream base, not the local checkpoint used by the baseline patch),source_sha256===source_after_sha256,source_files exact sorted Go fileProof inventory (name,sha256,bytes,git_blob_sha1 and executable:true only when executable), and sha256 of its exact canonical JSON must equal source_sha256. Match candidateInventory’s tracked+ordinary-untracked boundary, excluding only .runner-source-handoff.json and rejecting ignored consumed build inputs; do not substitute baseline projection digest. frontend.status=passed,typecheck_passed=true,handoff_sha256=SHA of unchanged source/.runner-source-handoff.json; cleanup.browser_closed/server_stopped/database_removed/build_scratch_removed all true. Carry actual relevant frontend proof or authenticated reuse, not fabricated flags. Additional current numbered/source-test-runtime results are allowed. summary.json schema:1,status:passed,source_sha256/source_after_sha256 identical,report:{file:"report.json",sha256:exactreportSHA}.',
    'Archive is one flat canonical evidence.tar.gz <=16MiB: regular unique entries only, no directory headers, ./ prefixes, symlinks or duplicate names; <=88 entries/84 PNGs, each <=2MiB, total uncompressed <=14MiB. Allowed names ONLY report.json,summary.json,browser.json,frontend-check.json and (chromium|webkit)-[a-z0-9-]+.png. Put numbered results/other metadata inside those JSON files, not extra entries. Use the prior archive as exact schema example. Before publishing passed proof run read-only compatibility probe: RUNNER_SUDOKU_ADMISSION --mode admit-followup --accepted-check OWNARCHIVE --accepted-check-sha256 ARCHIVESHA --accepted-handoff SOURCE/.runner-source-handoff.json --source CANONICALSOURCE --scratch UNIQUEOWNEDDIR. OMIT --request; no tests rerun or source writes. Require success, delete probe scratch and keep probed archive bytes UNCHANGED. domain.reason records probe passed and archive SHA. Failure routes author repair, never acceptance. No second archive.',
    'RUNNER_SUDOKU_PREVIEW_ROOT is process-owned, independent of this goal. Partition stopped builds ONLY under PREVIEW_ROOT/<SHA256 of exact --goal bytes>; bind each manifest to that exact goal plus source/test/server/runtime hashes. Authenticate ownership, use content identities and atomic promotion, retain one canonical executable with embedded assets per applicable goal, preserve existing valid previews until replacement succeeds and never alter another goal partition. Record running:false and no release authority. Never launch a retained preview daemon. Phone LAN exposure belongs to the trusted post-acceptance operator; test browser-native share/clipboard unavailable/cancel fallbacks honestly without posting.',
    'You may run syntax/synthetic helper checks, but no direct product tests/build/server/browser. Return compact JSON {status:"authored"|"blocked",entry:'+JSON.stringify(testEntry)+',changed_paths:[...],requirements:[actualgoalIDs],contract_checks:[...],gaps:[...]}. Separate fixed execution and independent source/UX review establish adequacy.'
  ]);
  const source=native('source',names.source,'Independently reconcile current correction IDs with implementation, test adequacy and authenticated evidence.',[
    'Inspect current source/test code, baseline delta and report/summary. Map every actual goal.corrections[] ID to meaningful assertions/observations. Audit script input/source hashes, canonical admission-compatible archive/probe and cleanup, not pass counts. Find omitted hard states, mocks, weakened assertions, duplicate gates, cosmetic fakes/hidden failures. Check dated official sharing/privacy claims where relevant, scope/state/accessibility/other-games preservation, unnecessary tests/copies and accumulating patch branches. Prefer a coherent cause-level correction.',
    'Only frontend/src, exact design doc and the explicit project verification surface may change. Source proofs compare baseline and current source/test hashes; valid unchanged frontend/build evidence may be reused. Read compact evidence first; use full arrays only for concrete questions. Do not run duplicate product checks or approve from counts alone. Owner-approved interaction changes supersede obsolete gates.',
    'Write source-assessment.json in request.output_dir: {verdict,source_sha256,test_sha256,owner_goal_sha256,requirements:[{id,verdict,evidence,gap}],findings:[{severity,path,issue,required_change}],gaps}. Explicitly reconcile EVERY owner ID with full evidence; omit none. Return outputs.assessment="source-assessment.json" plus report:text <=1024 bytes containing verdict/identities/finding counts and artifact reference only. Do not inline the reconciliation. Your source/assertion audit and matching proof establish source approval; visual judgment is independent. No unsupported release/native/physical claims.'
  ]);
  source.outputs.assessment='artifact';
  const visual=native('visual',names.visual,'Independently judge the complete corrected Sudoku experience and each owner requirement from current full-flow evidence.',[
    'Use this self-contained goal and puzzle-review guidance, not caller skill paths. A satisfying puzzle preserves mental context: clear board/digit/tool hierarchy, readable current state, predictable cell/pin/notes/history/hint actions, retained progress/focus across resize, brief useful explanations, discoverable calm secondary actions, and uninterrupted actual play. Preserve Jack’s Break wordmark/Manrope/indigo/lime/light-dark identity. Judge uncluttered composition, spacing, icons, legibility, enabled/disabled/focus states and logical next action as a player, not a metric auditor.',
    'Require current full/open-panel images and interaction receipts. Extract only to unique owned TMPDIR with finally cleanup. PERSONALLY view readable full-viewport PNGs using native image tool; filenames/DOM/older images are not substitutes. Inspect a representative full journey and materially different requested themes/compositions/states; compare owner captures and every actual goal.corrections[] ID, then judge whole experience. Functional claims need interaction proof, not image inference.',
    'Be independent of the test author, source reviewer and passing geometry: a polished aggregate test result can still miss the goal. Identify missing images/untested requirements honestly; do not approve an empty/failed archive or invent native Safari/physical-phone proof. Avoid repeating unchanged functional checks. Preserve original evidence, use small summaries and remove only owned extraction scratch.',
    'Write ux-assessment.json in request.output_dir: {verdict,source_sha256,test_sha256,owner_goal_sha256,requirements:[{id,verdict,images,interaction,gap}],inspected_images:[actually viewed names],findings:[{image,issue,required_change}],gaps}. Preserve full EVERY-ID reconciliation and actual image names there; never inline them in final response. Return outputs.assessment="ux-assessment.json" and report:text <=1024 bytes with verdict/identities/counts/artifact reference. Approval needs all current IDs and no unresolved whole-experience mismatch for SAME candidate. Trusted operator handles phone inspection; no release.'
  ]);
  visual.outputs.assessment='artifact';
  const baseline={id:names.baseline,purpose:'Authenticate and preserve the accepted source-only baseline once before any correction writer.',
    inputs:{owner_goal:'artifact',prior:'artifact',handoff:'artifact'},outputs:{check:'text',artifact:'artifact'},execution:{harness:'command',mode:'shared',access:'workspace-read',command:{
      args:[runtime.node,runtime.capture,'--source','{workspace}','--scratch','{scratch}','--admission',runtime.admission,
        '--accepted-check','{artifact.prior}','--accepted-handoff','{artifact.handoff}','--request','{artifact.owner_goal}'],
      env:{PATH:runtime.path},network:false,result:'domain.json',artifact:'baseline.tar.gz'
    }}};
  const check={id:names.check,purpose:'Execute the authored project verification independently against the read-only current candidate.',
    inputs:{owner_goal:'artifact',prior:'artifact',checks:'artifact'},outputs:{check:'text',artifact:'artifact'},execution:{harness:'command',mode:'shared',access:'workspace-read',command:{
      args:[runtime.node,'{workspace}/'+testEntry,'--source','{workspace}','--scratch','{scratch}','--goal','{artifact.owner_goal}',
        '--baseline','{dependency.baseline.artifact}','--prior','{artifact.prior}','--checks','{artifact.checks}'],
      env:{PATH:runtime.path,GOTOOLCHAIN:'local',GOCACHE:runtime.goCache,GOMODCACHE:runtime.goModuleCache,OPENSSL_CONF:runtime.opensslConf,RUNNER_GO_MODULES:runtime.goModuleCache,
        RUNNER_SUDOKU_GO:runtime.go,RUNNER_SUDOKU_MODULES:runtime.modules,RUNNER_SUDOKU_PLAYWRIGHT:runtime.playwright,
        RUNNER_SUDOKU_CHROMIUM:runtime.chromium,RUNNER_SUDOKU_WEBKIT:runtime.webkit,RUNNER_SUDOKU_FRONTEND_HELPER:runtime.frontendHelper,
        RUNNER_SUDOKU_ADMISSION:runtime.admission,
        RUNNER_SUDOKU_PREVIEW_ROOT:runtime.preview},
      runtime_write_paths:[...runtime.runtimeWritePaths,runtime.preview],network:true,result:'domain.json',artifact:'evidence.tar.gz'
    }}};
  // Network research is a separate explicit capability. Model/access/effort and
  // input/output bounds of every ordinary actor remain its accepted predecessor's.
  return [lead,baseline,research,author,developer,check,source,visual];
}

export function correctionProcess(parent) {
  correctionFacts(parent);
  const process=structuredClone(parent.process);
  process.id=processID;
  process.inputs={request:'text',owner_goal:'artifact',patch:'artifact',checks:'artifact',prior:'artifact',handoff:'artifact'};
  process.steps=[{id:'lead',role:names.lead,inputs:Object.fromEntries(Object.keys(process.inputs).map(name=>[name,{from_input:name}])),
    routes:{accepted:{next:'$complete'},rejected:{next:'$fail'},ready:{next:'$fail'}}}];
  process.outputs={result:{from_step:'lead',output:'report'}};
  process.tasks.roles=Object.values(names).filter(name=>name!==names.lead);
  return process;
}

export function goalStageRole(runtime) {
  return {id:goalStageRoleID,purpose:'Preserve exact owner-document bytes and digest only; no source admission or product approval.',
    inputs:{},outputs:{check:'text',artifact:'artifact'},execution:{harness:'command',mode:'shared',access:'workspace-read',command:{
      args:[runtime.node,runtime.goalStager,'--goal','{goal}','--scratch','{scratch}'],env:{PATH:runtime.path},network:false,
      result:'domain.json',artifact:'owner-goal.json'}}};
}

export function goalStageProcess(parent) {
  correctionFacts(parent);
  const process=structuredClone(parent.process);
  process.id=goalStageID;process.inputs={request:'text'};process.tasks.roles=[goalStageRoleID];
  process.steps=[{id:'stage',role:goalStageRoleID,inputs:{},routes:{ready:{next:'$complete'}}}];
  process.outputs={goal:{from_step:'stage',output:'artifact'},summary:{from_step:'stage',output:'check'}};
  return process;
}
