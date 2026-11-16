import assert from 'node:assert/strict';
import {chmod, mkdir, mkdtemp, readFile, realpath, rm, writeFile} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {test} from 'node:test';
import {configureFollowup, followupFacts, followupProcess, followupProcessID, followupRoles, overlayInstance} from './followup.mjs';

function acceptedParent(workspace = '/accepted/source') {
  const native = access => ({harness: 'native', access, model: 'actual-parent-model', reasoning: 'high',
    instructions: 'Current amended parent instructions. Read "/skills/product/SKILL.md".', max_input: '64KiB', max_output: '8KiB',
    native: {harness: 'codex', binary: '/tools/actual-codex', context: 'lean', tools: {mode: 'native'}, skills: {mode: 'none'}, network: false,
      runtime_write_paths: ['/cache/go', '/cache/modules']}});
  const parent = {
    id: 'run_accepted', project_id: 'sudoku-preview', status: 'Completed', revision: 987, resources_released: true, repository: workspace,
    task_graph: {root_run_id: 'run_accepted', task_id: 'root', children: [{run_id: 'run_import'}, {run_id: 'run_check'}]},
    attempts: [{id: 'attempt_accepted', state: 'finished', released: true}],
    inputs: {patch: {artifact_id: 'attempt_original/changes.patch'}, checks: {artifact_id: 'attempt_original_check/result'}},
    process: {id: 'continue-sudoku-preview', max_attempts: 128, max_attempts_per_step: 4, infrastructure_retries: 1, attempt_timeout: '20m',
      tasks: {roles: ['sudoku-developer'], max_tasks: 64, max_depth: 2, max_revisions: 0, max_concurrent: 2},
      steps: [{id: 'lead', role: 'sudoku-lead'}], outputs: {result: {from_step: 'lead', output: 'report'}}},
    roles: {}
  };
  for (const id of ['sudoku-lead', 'sudoku-developer', 'sudoku-source-review', 'sudoku-ux-review']) {
    parent.roles[id] = {id, purpose: id, inputs: {}, outputs: {report: 'text'}, execution: native(id === 'sudoku-developer' ? 'workspace-write' : 'workspace-read')};
  }
  parent.roles['sudoku-check'] = {id: 'sudoku-check', inputs: {checks: 'artifact'}, outputs: {check: 'text', artifact: 'artifact'},
    execution: {harness: 'command', mode: 'shared', access: 'workspace-read', command: {
      args: ['/tools/old-checker', '--source', '{workspace}', '--scratch', '{scratch}', '--request', '/accepted/request.json',
        '--preview', '/accepted/preview', '--node', '/tools/actual-node', '--modules', '/qualified/node_modules',
        '--handoff', '{dependency.import.artifact}', '--prior-check', '{artifact.checks}'],
      env: {PATH: '/tools:/usr/bin:/bin', RUNNER_SUDOKU_FRONTEND_HELPER: '/tools/qualified-frontend-check'},
      runtime_write_paths: ['/cache/go', '/accepted/preview'], result: 'domain.json', artifact: 'evidence.tar.gz', network: true
    }}};
  return parent;
}

test('follow-up staging requires a completed settled root with actual amended profiles', () => {
  const parent = acceptedParent();
  assert.equal(followupFacts(parent).patch, 'attempt_original/changes.patch');
  for (const change of [
    p => { p.status = 'Failed'; },
    p => { p.resources_released = false; },
    p => { p.task_graph.parent_run_id = 'run_other'; },
    p => { p.attempts[0].released = false; },
    p => { p.roles['sudoku-developer'].execution = undefined; },
    p => { p.inputs.patch.artifact_id = '../private/auth'; }
  ]) {
    const changed = structuredClone(parent);
    change(changed);
    assert.throws(() => followupFacts(changed));
  }
});

test('follow-up roles preserve harness capabilities and route updated evidence before review', () => {
  const parent = acceptedParent(), before = structuredClone(parent);
  const runtime = {checker: '/qualified/new-checker', preview: '/followup/preview'};
  const roles = followupRoles(parent, runtime), byID = Object.fromEntries(roles.map(r => [r.id, r]));
  assert.deepEqual(parent, before);
  for (const [oldID, newID] of [
    ['sudoku-lead', 'sudoku-followup-lead'], ['sudoku-developer', 'sudoku-followup-developer'],
    ['sudoku-source-review', 'sudoku-followup-source-review'], ['sudoku-ux-review', 'sudoku-followup-ux-review']
  ]) {
    const actual = structuredClone(byID[newID].execution);
    actual.instructions = parent.roles[oldID].execution.instructions;
    assert.deepEqual(actual, parent.roles[oldID].execution);
    assert.deepEqual(byID[newID].outputs, {report: 'text'});
    assert.ok(Buffer.byteLength(JSON.stringify(byID[newID], null, 2)) < 16 << 10);
  }
  const admit = byID['sudoku-followup-admit'].execution;
  assert.equal(admit.access, 'workspace-read');
  assert.equal(admit.command.network, false);
  assert.deepEqual(admit.command.args, ['/qualified/new-checker', '--mode', 'admit-followup', '--source', '{workspace}', '--scratch', '{scratch}',
    '--request', '{goal}', '--accepted-check', '{artifact.prior}', '--accepted-handoff', '{artifact.handoff}']);
  const checked = byID['sudoku-followup-check'].execution.command;
  assert.equal(checked.args[checked.args.indexOf('--request') + 1], '{goal}');
  assert.equal(checked.args[checked.args.indexOf('--preview-root') + 1], runtime.preview);
  assert.deepEqual(checked.runtime_write_paths, ['/cache/go', runtime.preview]);
  assert.ok(checked.args.includes('{dependency.import.artifact}'));
  const lead = byID['sudoku-followup-lead'].execution.instructions;
  assert.match(lead, /never pre-admit reviews behind unfinished checks/i);
  assert.match(lead, /all request-specific feedback checks/);
  assert.match(lead, /Do not stop or ask for an aggregate allowance/);
  assert.doesNotMatch(lead, /36 historical attempts|twenty child slots|first visit admit only two children/);
  const process = followupProcess(parent);
  assert.equal(process.id, followupProcessID);
  assert.deepEqual(process.inputs, {request: 'text', patch: 'artifact', checks: 'artifact', prior: 'artifact', handoff: 'artifact'});
  assert.equal(process.attempt_timeout, parent.process.attempt_timeout);
  assert.equal(process.max_attempts_per_step, parent.process.max_attempts_per_step);
  assert.equal(process.tasks.max_concurrent, parent.process.tasks.max_concurrent);
  assert.equal(process.steps[0].routes.ready.next, '$fail');
  const nextParent = {...parent, id: 'run_followup', task_graph: {root_run_id: 'run_followup'},
    process, roles: Object.fromEntries(roles.map(role => [role.id, role]))};
  const nextRoles = followupRoles(nextParent, runtime);
  assert.deepEqual(nextRoles.map(role => role.execution.command?.args), roles.map(role => role.execution.command?.args));
});

test('overlay preserves instance policy, credentials and old projects while replacing one project reference', () => {
  const original = {projects: ['jacks.json', 'sudoku.json'], enforce_work_limits: false, concurrency: 1, max_active_attempts: 2,
    storage: {max_managed: '8GiB'}, codex: {binary: './runtime/codex', auth_file: 'private/auth.json', command_read_paths: ['runtime/tools']},
    release: {command: ['/protected/release', '--policy', '/policy.json']}};
  const result = overlayInstance(original, '/live/config/instance.json', '/live/config/sudoku.json', '/new/config/sudoku.json');
  assert.deepEqual(result.projects, ['/live/config/jacks.json', '/new/config/sudoku.json']);
  for (const key of ['enforce_work_limits', 'concurrency', 'max_active_attempts', 'storage', 'release']) assert.deepEqual(result[key], original[key]);
  assert.equal(result.codex.auth_file, '/live/config/private/auth.json');
  assert.equal(result.codex.binary, '/live/config/runtime/codex');
  assert.deepEqual(result.codex.command_read_paths, ['/live/config/runtime/tools']);
  assert.deepEqual(original.projects, ['jacks.json', 'sudoku.json']);
  assert.throws(() => overlayInstance(original, '/live/config/instance.json', '/absent.json', '/new.json'));
});

test('builder stages exact owner request and preserved references without writing accepted source or live config', async () => {
  const temp = await realpath(await mkdtemp(path.join(os.tmpdir(), 'runner-followup-config-')));
  try {
    const workspace = path.join(temp, 'source'), config = path.join(temp, 'old-config'), root = path.join(temp, 'new');
    await mkdir(workspace);
    await mkdir(config);
    const sentinel = path.join(workspace, 'keep.txt');
    await writeFile(sentinel, 'accepted source');
    const checker = path.join(temp, 'checker');
    await writeFile(checker, '#!/bin/sh\nexit 0\n');
    await chmod(checker, 0o700);
    const project = {id: 'sudoku-preview', workspace, default_process: 'continue-sudoku-preview',
      processes: {'continue-sudoku-preview': 'process.json'}, roles: {'sudoku-lead': {file: 'old-lead.json'}}};
    const projectFile = path.join(config, 'project.json');
    const instanceFile = path.join(config, 'instance.json');
    const projectBytes = JSON.stringify(project), instanceBytes = JSON.stringify({projects: ['project.json'], concurrency: 1, enforce_work_limits: false});
    await writeFile(projectFile, projectBytes);
    await writeFile(instanceFile, instanceBytes);
    const inspectFile = path.join(temp, 'inspect.json'), requestFile = path.join(temp, 'request.json');
    await writeFile(inspectFile, JSON.stringify(acceptedParent(workspace)));
    const requestBytes = JSON.stringify({request: 'Preserve the original goal; apply these owner corrections. 🙂',
      accepted_candidate: {check_artifact: 'attempt_accepted_check/result', check_sha256: 'a'.repeat(64), handoff_artifact: 'attempt_import/result'}}, null, 2) + '\n';
    await writeFile(requestFile, requestBytes);
    const result = await configureFollowup({root, instance: instanceFile, 'parent-inspect': inspectFile, 'request-file': requestFile,
      checker, 'prior-artifact': 'attempt_accepted_check/result', 'handoff-artifact': 'attempt_import/result', 'prior-sha256': 'a'.repeat(64)});
    assert.equal(result.submitted, false);
    assert.equal(result.workspace, workspace);
    assert.equal(await readFile(sentinel, 'utf8'), 'accepted source');
    assert.equal(await readFile(projectFile, 'utf8'), projectBytes);
    assert.equal(await readFile(instanceFile, 'utf8'), instanceBytes);
    const stagedProject = JSON.parse(await readFile(result.project_file, 'utf8'));
    assert.equal(stagedProject.default_process, project.default_process);
    assert.equal(stagedProject.processes['continue-sudoku-preview'], path.join(config, 'process.json'));
    assert.deepEqual(stagedProject.roles['sudoku-lead'], {file: path.join(config, 'old-lead.json')});
    const request = JSON.parse(await readFile(result.request, 'utf8'));
    assert.equal(request.inputs.request, requestBytes);
    assert.deepEqual(request.followup, {run_id: 'run_accepted', revision: 987});
    assert.equal(request.artifacts.patch, 'attempt_original/changes.patch');
    assert.equal(request.artifacts.prior, 'attempt_accepted_check/result');
    const stagedChecker = JSON.parse(await readFile(stagedProject.roles['sudoku-followup-check'].file, 'utf8'));
    assert.equal(stagedChecker.execution.command.args[stagedChecker.execution.command.args.indexOf('--request') + 1], '{goal}');
    await assert.rejects(configureFollowup({root}), /non-empty root/);
  } finally {
    await rm(temp, {recursive: true, force: true});
  }
});
