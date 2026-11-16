#!/usr/bin/env node
import {constants, createReadStream} from 'node:fs';
import {access, chmod, lstat, mkdir, readFile, realpath, stat, writeFile} from 'node:fs/promises';
import {createHash} from 'node:crypto';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {parseArgs} from 'node:util';
import {emptyOwnedRoot} from './configure.mjs';

export const followupProcessID = 'followup-sudoku-preview';
const names = Object.freeze({
  lead: 'sudoku-followup-lead', admit: 'sudoku-followup-admit', check: 'sudoku-followup-check',
  developer: 'sudoku-followup-developer', source: 'sudoku-followup-source-review', visual: 'sudoku-followup-ux-review'
});
const hash = data => createHash('sha256').update(data).digest('hex');
const inside = (root, value) => value === root || value.startsWith(root + path.sep);
const artifactID = id => typeof id === 'string' && /^[A-Za-z0-9_-]+\/[A-Za-z0-9_.-]+$/.test(id);

function absolute(value, name) {
  if (typeof value !== 'string' || !path.isAbsolute(value) || value.includes('\0')) throw new Error(name + ' requires an absolute path');
  return path.resolve(value);
}

async function boundedFile(filename, limit) {
  const info = await lstat(filename);
  if (!info.isFile() || info.isSymbolicLink() || info.size > limit) throw new Error('bounded regular input required: ' + filename);
  const data = await readFile(filename);
  if (data.length !== info.size || data.length > limit) throw new Error('input changed during bounded read: ' + filename);
  return data;
}

export function followupFacts(parent) {
  if (!parent.id || parent.status !== 'Completed' || !parent.resources_released || parent.cancel_requested || parent.cleanup_error || parent.cleanup_blocked ||
      !Number.isSafeInteger(parent.revision) || parent.revision < 1 || parent.task_graph?.root_run_id !== parent.id || parent.task_graph.parent_run_id ||
      !Array.isArray(parent.attempts) || parent.attempts.some(a => a.state !== 'finished' || !a.released) || !parent.process?.tasks || !path.isAbsolute(parent.repository || '')) {
    throw new Error('follow-up needs a completed, settled general root inspect');
  }
  const nativeRoles = Object.fromEntries([
    ['lead', 'sudoku-lead'], ['developer', 'sudoku-developer'], ['source', 'sudoku-source-review'], ['visual', 'sudoku-ux-review']
  ].map(([kind, original]) => [kind, parent.roles?.[names[kind]] ? names[kind] : original]));
  for (const id of Object.values(nativeRoles)) {
    const p = parent.roles?.[id]?.execution;
    if (p?.harness !== 'native' || !p.native || !p.model || !p.instructions) throw new Error('amended native profile missing: ' + id);
  }
  const checker = (parent.roles?.[names.check] || parent.roles?.['sudoku-check'])?.execution;
  if (checker?.harness !== 'command' || checker.mode !== 'shared' || checker.access !== 'workspace-read' || !Array.isArray(checker.command?.args) ||
      checker.command.args.length < 1 || !checker.command.env?.RUNNER_SUDOKU_FRONTEND_HELPER) throw new Error('saved protected checker profile is missing');
  const patch = parent.inputs?.patch?.artifact_id, checks = parent.inputs?.checks?.artifact_id;
  if (!artifactID(patch) || !artifactID(checks)) throw new Error('accepted parent lacks its authenticated original artifact inputs');
  return {patch, checks, checker, nativeRoles};
}

function commandArgument(args, flag) {
  const index = args.indexOf(flag);
  if (index < 0 || index + 1 >= args.length || args.indexOf(flag, index + 1) !== -1) throw new Error('saved checker needs exactly one ' + flag);
  return args[index + 1];
}

function replacedArgument(args, flag, value) {
  commandArgument(args, flag);
  const result = [...args];
  result[result.indexOf(flag) + 1] = value;
  return result;
}

export function followupRoles(parent, runtime) {
  const {checker, nativeRoles} = followupFacts(parent);
  const common = [
    'The complete task_context.goal is authoritative. Read its original agreement and latest explicit owner amendments; latest amendments supersede earlier requirements only where they conflict. All children inherit this complete goal. This is linked follow-up work in the existing canonical project workspace: the accepted parent, source, failed history and proof remain preserved. Do not reinitialize or restore an older snapshot.',
    'Work only inside this assigned Runner task. Do not spawn outside agents, invoke Runner controls, alter protected helpers/configuration, copy source/dependencies/caches, request routine permission, merge, deploy, restore a database or waive release gates. Return report:text; use outcome ready for a delivered report even if its verdict rejects the candidate. Do not invent artifact IDs, checks or approval.',
    'Use canonical artifacts from request.evidence_files and actual declared dependency results. A child Completed/ready means its report was delivered, not that the product passed. Read compact protected summary.json and relevant failures; never dump whole archives, patches, reports or source hash arrays into model context. Complete proof remains canonical, not copied into task specifications.',
    'Read effective task_context.attempt_budget.unlimited and task_context.unlimited_tasks. Remaining attempts/tasks of -1 mean unlimited; historical finite fields/prose are accounting, not active limits. Do not stop or ask for an aggregate allowance while effective limits are disabled. Local retries, depth, access, concurrency, timeouts, storage and actual acceptance still apply.'
  ].join('\n\n');
  const native = (kind, id, purpose, inputs, instructions) => {
    const role = structuredClone(parent.roles[nativeRoles[kind]]);
    role.id = id;
    role.purpose = purpose;
    role.inputs = inputs;
    role.outputs = {report: 'text'};
    role.execution.instructions = common + '\n\n' + instructions.join('\n\n');
    return role;
  };
  const lead = native('lead', names.lead, 'Own the linked Sudoku follow-up from accepted source through independently verified preview.',
    {request: 'text', patch: 'artifact', checks: 'artifact', prior: 'artifact', handoff: 'artifact'}, [
      'The source admission task ID is import. On the first visit delegate import using role ' + names.admit + ', inputs prior/handoff copied from your assigned artifact inputs, write_paths:[]; it verifies exact accepted current source and forwards the original handoff without restoring files. Then delegate one ' + names.developer + ' task depending on import, inputs prior copied from your artifact input, write_paths:["frontend/src","docs/sudoku-interface.md"], to implement the current owner corrections and focused tests. A protected ' + names.check + ' task may depend on that developer and import, with original checks:artifact input and write_paths:[]. These three tasks may be admitted together; known requested corrections do not need another baseline build.',
      'After actual checker output arrives, inspect its delivered check:text and compact summary.json. A failed/blocked domain report is actionable failure evidence. Assign another coherent scoped developer correction and protected check when needed; reconsider the cause/design after repeated failures. Do not treat collector failures as product bugs or make speculative source edits. Fixed checks always depend on import and the developer they verify. Keep task IDs/results and accepted unrelated work; delegate rather than retire or revise history.',
      'Only after observing current passing frontend/typecheck/build and complete browser proof admit independent source and visual reviews. Source role ' + names.source + ' receives original patch/checks and accepted prior artifacts; visual role ' + names.visual + ' receives prior:artifact. Both are read-only and depend on import plus the actual latest protected checker; they may run in parallel. Never pre-admit reviews behind unfinished checks. Use only relevant current evidence/findings rather than the entire historical chain.',
      'Keep briefs at most400 characters, with scope/purpose and at most one concrete failure sentence; use two compact observable acceptance conditions. Actual reports flow through task_context.results, never copied into specifications. Control actions return outcome ready, task_action.kind delegate and empty outputs. Final task_action is null, outcome accepted or rejected; final ready without an action fails.',
      'Accept only the full updated goal. Latest protected summary.status must be passed, source_sha256===source_after_sha256, frontend.status passed and typecheck_passed true; all five build stages and current browser proof must pass. Require two engines,146geometry rows,20journeys, three neighbor smokes with zero failures, plus all request-specific feedback checks. Require cleanup.browser_closed/server_stopped/database_removed/build_scratch_removed true and retained stopped preview executable/manifest hashes for this exact source. Independent source and personally viewed visual reports must approve that SAME source SHA. Old accepted proof identifies the starting candidate; it is not acceptance after changes.',
      'A source diagnostic can resolve a concrete failed-proof question, but final reviews require ready evidence. Native Safari/physical-device gaps stay explicit for this local emulated preview. Reject only a concrete unresolved requirement/proof gap or unsupported capability/authority. Final report at most350words: outcome, exact source SHA, protected archive ID, retained preview executable/manifest paths+hashes, verified visible/function changes, cleanup and material gaps. No merge/deployment/restoration/native-gate waiver; do not claim a running preview URL.'
    ]);
  const developer = native('developer', names.developer, 'Implement one coherent owner-requested Sudoku correction with focused evidence.', {prior: 'artifact'}, [
    'Read the full updated goal, source admission result, accepted prior evidence and relevant current source. Change only frontend/src and, if useful, docs/sudoku-interface.md. Preserve other puzzles, generated .gny files, .genny, dependencies/locks, backend, evidence fixtures, Git metadata and .runner-source-handoff.json. The source is already the accepted parent candidate; do not reapply the old patch or regenerate it.',
    'Fix causes coherently, including obsolete test drivers/expectations after an approved interaction change while preserving the underlying regression properties. Validate the smallest relevant existing tests before returning through the qualified helper ' + JSON.stringify(checker.command.env.RUNNER_SUDOKU_FRONTEND_HELPER) + ' with --tests (JSON array), --serial, --node ' + JSON.stringify(commandArgument(checker.command.args, '--node')) + ' --modules ' + JSON.stringify(commandArgument(checker.command.args, '--modules')) + ', current workspace as --source and a unique owned scratch_dir subdirectory as --scratch. Read domain/report status and actual failures. Correct scoped failures and rerun affected tests only when their inputs change; retain compact counts/status before cleaning scratch.',
    'Do not repeat unchanged passing checks, full acceptance gates, builds, package-manager scripts, servers or browser automation. Independent ' + names.check + ' owns final frontend/typecheck/build/browser proof. Focused developer evidence does not replace it. Return compact JSON in report:text {status:"changed"|"blocked",changed_paths:[...],summary:"...",focused_checks:[...],remaining_gaps:[...]}; preserve actual source edits and clean only owned scratch.'
  ]);
  const source = native('source', names.source, 'Independently assess exact current follow-up source and authenticated frontend proof.',
    {patch: 'artifact', checks: 'artifact', prior: 'artifact'}, [
      'Authenticate the current compact protected summary and source/frontend hashes, source admission handoff and accepted prior proof. Inspect targeted current hunks and relevant original patch lineage; never dump the whole patch or source inventory. Do not assume old frontend proof applies after source changes. The original handoff establishes initial projection/commit; prior accepted source establishes this follow-up baseline.',
      'Review current scope, state transitions, accessibility/input behavior, ownership, unrelated puzzle preservation and regression assertions. Prefer a coherent simpler fix over accumulating exception branches. A changed UI may justify changed locators/expectations while preserving the original regression property. Do not run duplicate product tests. Your own source review and exact authenticated frontend proof establish source approval; browser/visual and whole-goal approval are separate.',
      'Return compact JSON in report:text {verdict:"approved"|"changes_requested"|"blocked",source_sha256:"exact current SHA",findings:[{severity,path,issue,required_change}],gaps:[...]}. Preserve exact current proof; do not reinterpret older blocked reports as approval or certify physical/native coverage from source.'
  ]);
  const skillPaths = [...new Set([...parent.roles[nativeRoles.visual].execution.instructions.matchAll(/"([^"\n]*\/SKILL\.md)"/g)].map(m => m[1]))];
  const visual = native('visual', names.visual, 'Independently judge the actual updated Sudoku experience from current full-viewport evidence.', {prior: 'artifact'}, [
    skillPaths.length ? 'Read these existing review skills directly without enabling inherited catalogs: ' + skillPaths.map(p => JSON.stringify(p)).join(', ') + '. Apply them within this role and current owner goal; do not launch outside research, browsers, servers, tests or agents.' : 'Apply the assigned product/puzzle experience judgment within this read-only evidence review; do not launch outside research, browsers, servers, tests or agents.',
    'Require actual current images and interaction evidence; a Completed checker may have delivered only failure diagnostics. Filter browser.json to image metadata and relevant journey results. Extract only to a unique owned TMPDIR directory with finally/trap cleanup. PERSONALLY view every retained full-viewport target PNG with the native image tool. Metrics, DOM descriptions and older accepted images are not substitutes.',
    'Judge all latest owner amendments against real initial, selected/pinned, notes, hint, completed and applicable request-specific states across declared phone/Duo/tablet/desktop light/dark coverage. Assess brand, hierarchy, uncluttered controls, board/control balance, spacing, icon quality, legibility, focus/input affordances and continuity. Compare exact source_sha256 and request_sha256; use interaction receipts for behavior, not visual inference. Avoid duplicate functional testing.',
    'Return compact JSON in report:text {verdict:"approved"|"changes_requested"|"blocked",source_sha256:"exact current SHA",request_sha256:"exact request SHA",inspected_images:["names actually viewed"],findings:[{image,issue,required_change}],gaps:[...]}. Approval needs personally viewed current images and no material mismatch with the updated goal. Disclose emulation, native/physical/accessibility gaps without inventing a blanket physical-device requirement. Clean owned extraction scratch.'
  ]);
  const admit = {
    id: names.admit, purpose: 'Authenticate the accepted current source and forward its unchanged original handoff before follow-up writes.',
    inputs: {prior: 'artifact', handoff: 'artifact'}, outputs: {check: 'text', artifact: 'artifact'},
    execution: {
      harness: 'command', mode: 'shared', access: 'workspace-read',
      command: {
        args: [runtime.checker, '--mode', 'admit-followup', '--source', '{workspace}', '--scratch', '{scratch}', '--request', '{goal}',
          '--accepted-check', '{artifact.prior}', '--accepted-handoff', '{artifact.handoff}'],
        env: {PATH: checker.command.env.PATH}, network: false, result: 'domain.json', artifact: 'handoff.json'
      }
    }
  };
  const check = {id: names.check, purpose: 'Collect protected current-source functional, visual, owner-feedback and cleanup proof.',
    inputs: {checks: 'artifact'}, outputs: {check: 'text', artifact: 'artifact'}, execution: structuredClone(checker)};
  const previewFlag = check.execution.command.args.includes('--preview-root') ? '--preview-root' : '--preview';
  const previousPreview = commandArgument(check.execution.command.args, previewFlag);
  check.execution.command.args[0] = runtime.checker;
  check.execution.command.args = replacedArgument(check.execution.command.args, '--request', '{goal}');
  check.execution.command.args = replacedArgument(check.execution.command.args, previewFlag, runtime.preview);
  check.execution.command.args[check.execution.command.args.indexOf(previewFlag)] = '--preview-root';
  const roots = check.execution.command.runtime_write_paths || [];
  if (!roots.includes(previousPreview)) throw new Error('saved checker preview write claim is missing');
  check.execution.command.runtime_write_paths = roots.map(p => p === previousPreview ? runtime.preview : p);
  return [admit, lead, check, developer, source, visual];
}

export function followupProcess(parent) {
  const process = structuredClone(parent.process);
  process.id = followupProcessID;
  process.inputs = {request: 'text', patch: 'artifact', checks: 'artifact', prior: 'artifact', handoff: 'artifact'};
  process.steps = [{id: 'lead', role: names.lead, inputs: Object.fromEntries(Object.keys(process.inputs).map(name => [name, {from_input: name}])),
    routes: {accepted: {next: '$complete'}, rejected: {next: '$fail'}, ready: {next: '$fail'}}}];
  process.outputs = {result: {from_step: 'lead', output: 'report'}};
  process.tasks.roles = [names.admit, names.check, names.developer, names.source, names.visual];
  return process;
}

export function overlayInstance(instance, instanceFile, selectedProject, overlayProject) {
  const result = structuredClone(instance), base = path.dirname(instanceFile);
  let matches = 0;
  result.projects = result.projects.map(p => {
    const resolved = path.resolve(base, p);
    if (resolved === selectedProject) { matches++; return overlayProject; }
    return resolved;
  });
  if (matches !== 1) throw new Error('selected project must occur exactly once in the instance');
  if (result.codex) {
    for (const key of ['binary', 'go_binary']) if (result.codex[key]?.includes(path.sep)) result.codex[key] = path.resolve(base, result.codex[key]);
    for (const key of ['auth_file', 'go_module_cache', 'go_check_binary']) if (result.codex[key]) result.codex[key] = path.resolve(base, result.codex[key]);
    if (result.codex.command_read_paths) result.codex.command_read_paths = result.codex.command_read_paths.map(p => path.resolve(base, p));
  }
  return result;
}

async function jsonFile(filename, value, limit = 16 << 10) {
  const data = JSON.stringify(value, null, 2) + '\n';
  if (Buffer.byteLength(data) > limit) throw new Error('generated document exceeds its byte bound: ' + filename);
  await writeFile(filename, data, {mode: 0o600, flag: 'wx'});
}

export async function configureFollowup(values) {
  const root = await emptyOwnedRoot(values.root);
  const instanceFile = await realpath(absolute(values.instance, '--instance'));
  const inspectFile = await realpath(absolute(values['parent-inspect'], '--parent-inspect'));
  const requestFile = await realpath(absolute(values['request-file'], '--request-file'));
  const checker = await realpath(absolute(values.checker, '--checker'));
  const preview = absolute(values.preview || path.join(root, 'preview'), '--preview');
  if (!inside(root, preview)) throw new Error('preview must be owned by the new follow-up root');
  if (!artifactID(values['prior-artifact']) || !artifactID(values['handoff-artifact']) || !/^[a-f0-9]{64}$/.test(values['prior-sha256'] || '')) throw new Error('exact accepted artifact IDs and --prior-sha256 are required');
  const instanceBytes = await boundedFile(instanceFile, 16 << 10), inspectBytes = await boundedFile(inspectFile, 16 << 20);
  const requestBytes = await boundedFile(requestFile, 128 << 10);
  const goal = JSON.parse(requestBytes);
  if (goal.accepted_candidate?.check_sha256 !== values['prior-sha256'] || goal.accepted_candidate.check_artifact !== values['prior-artifact'] ||
      goal.accepted_candidate.handoff_artifact !== values['handoff-artifact']) throw new Error('owner goal and selected accepted artifact identities differ');
  const instance = JSON.parse(instanceBytes), parent = JSON.parse(inspectBytes), facts = followupFacts(parent);
  const workspace = await realpath(parent.repository);
  if (inside(workspace, root) || inside(root, workspace)) throw new Error('follow-up configuration/preview must not overlap the accepted workspace');
  const info = await stat(checker);
  if (!info.isFile()) throw new Error('checker must be a regular executable');
  await access(checker, constants.X_OK);
  const digest = createHash('sha256');
  for await (const chunk of createReadStream(checker)) digest.update(chunk);
  let project, projectFile;
  for (const ref of instance.projects || []) {
    const file = path.resolve(path.dirname(instanceFile), ref), p = JSON.parse(await boundedFile(file, 16 << 10));
    if (p.id === parent.project_id) {
      if (project) throw new Error('duplicate accepted project ID');
      project = p; projectFile = file;
    }
  }
  if (!project || await realpath(path.resolve(path.dirname(projectFile), project.workspace)) !== workspace) throw new Error('accepted parent and configured canonical workspace differ');
  if (project.processes?.[followupProcessID] || Object.values(names).some(id => project.roles?.[id])) throw new Error('follow-up configuration already exists; preserve it and rejoin its request');
  const roles = followupRoles(parent, {checker, preview});
  const process = followupProcess(parent), config = path.join(root, 'config'), overlayProject = path.join(config, 'projects', parent.project_id + '.json');
  const preserved = structuredClone(project);
  preserved.workspace = workspace;
  preserved.processes = Object.fromEntries(Object.entries(project.processes).map(([id, ref]) => [id, path.resolve(path.dirname(projectFile), ref)]));
  preserved.roles = Object.fromEntries(Object.entries(project.roles).map(([id, ref]) => [id, {...ref, file: path.resolve(path.dirname(projectFile), ref.file)}]));
  preserved.processes[process.id] = path.join(config, 'processes', process.id + '.json');
  for (const role of roles) preserved.roles[role.id] = {file: path.join(config, 'roles', role.id + '.json')};
  const prepared = overlayInstance(instance, instanceFile, projectFile, overlayProject);
  const documents = [[path.join(config, 'instance.json'), prepared], [overlayProject, preserved],
    [path.join(config, 'processes', process.id + '.json'), process], ...roles.map(role => [path.join(config, 'roles', role.id + '.json'), role])];
  for (const [file, document] of documents) if (Buffer.byteLength(JSON.stringify(document, null, 2) + '\n') > 16 << 10) throw new Error('configuration exceeds the loader16KiB bound: ' + file);
  for (const directory of [root, preview, config, path.join(config, 'projects'), path.join(config, 'roles'), path.join(config, 'processes'), path.join(root, 'requests')]) await mkdir(directory, {recursive: true, mode: 0o700});
  await chmod(root, 0o700);
  for (const [file, document] of documents) await jsonFile(file, document);
  const requestID = values['request-id'] || 'followup-' + parent.id + '-' + hash(requestBytes).slice(0, 24);
  const request = {request_id: requestID, project: parent.project_id, process: process.id,
    followup: {run_id: parent.id, revision: parent.revision}, inputs: {request: requestBytes.toString('utf8')},
    artifacts: {patch: facts.patch, checks: facts.checks, prior: values['prior-artifact'], handoff: values['handoff-artifact']}};
  await jsonFile(path.join(root, 'requests', 'followup.json'), request, 256 << 10);
  const runtime = {schema: 1, prepared_at: new Date().toISOString(), machine: os.hostname(), parent_run: parent.id, parent_revision: parent.revision,
    original_instance: {path: instanceFile, sha256: hash(instanceBytes)}, original_parent_inspect: {path: inspectFile, sha256: hash(inspectBytes)},
    request: {path: requestFile, sha256: hash(requestBytes), bytes: requestBytes.length}, accepted_check: {id: values['prior-artifact'], sha256: values['prior-sha256']},
    accepted_handoff: values['handoff-artifact'], workspace, preview, checker: {path: checker, sha256: digest.digest('hex'), bytes: info.size},
    instance: path.join(config, 'instance.json'), project_file: overlayProject, process: process.id, request_file: path.join(root, 'requests', 'followup.json'),
    authority: 'Staging only. Core Start revalidates the completed parent and exact family revision; fixed admission authenticates accepted current source before writes. No source/history copying, submission, installation or release approval.'};
  await jsonFile(path.join(root, 'runtime.json'), runtime, 128 << 10);
  return {config: runtime.instance, project_file: overlayProject, original_project_file: projectFile, request: runtime.request_file, runtime: path.join(root, 'runtime.json'),
    project: parent.project_id, process: process.id, workspace, preview, parent_run: parent.id, parent_revision: parent.revision, request_id: requestID, submitted: false};
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const {values} = parseArgs({options: Object.fromEntries(['instance', 'parent-inspect', 'root', 'request-file', 'checker', 'preview', 'prior-artifact', 'handoff-artifact', 'prior-sha256', 'request-id'].map(key => [key, {type: 'string'}]))});
    process.stdout.write(JSON.stringify(await configureFollowup(values), null, 2) + '\n');
  } catch (error) {
    process.stderr.write(error.message + '\n');
    process.exitCode = 1;
  }
}
