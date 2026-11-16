import assert from 'node:assert/strict';
import {mkdtemp, readFile, readdir, realpath, rm, symlink, writeFile} from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {test} from 'node:test';
import {fileURLToPath} from 'node:url';
import {emptyOwnedRoot, retained, retainedFacts, resolveInstance} from './configure.mjs';
import {roleDefinitions} from './roles.mjs';

function savedFacts() {
  const source = {attempt_id: retained.sourceAttempt, base: retained.base, tree: retained.tree,
    repository: '/owned/old-source', work_dir: '/owned/state/work/old/source', patch_id: retained.sourceAttempt + '/changes.patch'};
  const check = {...source, attempt_id: retained.checkAttempt, input_patch_id: source.patch_id};
  delete check.tree; // Actual legacy read-only checks do not attest a new tree.
  delete check.patch_id;
  return {
    id: retained.run, project_id: 'jacks-break', status: 'Failed', resources_released: true, revision: 87,
    repository: source.repository,
    process: {id: 'deliver-planned-frontend', max_attempts: 64, steps: [{id: 'owner'}, {id: 'check', role: 'fixed-check'}]},
    roles: {'fixed-check': {execution: {harness: 'command', command: {args: ['/owned/runner-frontend-check']}}}},
    inputs: {tests: {inline: JSON.stringify(Array.from({length: 12}, (_, i) => 'src/test-' + i + '.test.ts'))}},
    frontend_check_recovery: {serial: true, request_id: 'old-grant', patch_id: source.patch_id,
      patch_sha256: 'a'.repeat(64), helper_sha256: 'b'.repeat(64), check_step: 'check', check_visit: 4},
    attempts: [
      {id: retained.sourceAttempt, state: 'finished', released: true, source,
        end: {kind: 'returned', result: {outcome: 'ready', outputs: {patch: {artifact_id: source.patch_id}}}}},
      {id: retained.checkAttempt, step: 1, visit: 4, state: 'finished', released: true, source: check,
        end: {kind: 'returned', result: {outcome: 'ready', outputs: {artifact: {artifact_id: retained.checkAttempt + '/result'}}}}}
    ]
  };
}

test('setup refuses an occupied or symlink root and preserves existing files', async () => {
  const temp = await mkdtemp(path.join(os.tmpdir(), 'runner-sudoku-config-'));
  try {
    await writeFile(path.join(temp, 'keep.txt'), 'existing source', {mode: 0o600});
    await assert.rejects(emptyOwnedRoot(temp), /non-empty root/);
    assert.equal(await readFile(path.join(temp, 'keep.txt'), 'utf8'), 'existing source');
    assert.deepEqual(await readdir(temp), ['keep.txt']);
    await symlink(temp, path.join(temp, 'alias'));
    await assert.rejects(emptyOwnedRoot(path.join(temp, 'alias')), /real directory/);
    const unused = path.join(temp, 'new-config');
    assert.equal(await emptyOwnedRoot(unused), path.join(await realpath(temp), 'new-config'));
    await assert.rejects(readFile(unused), {code: 'ENOENT'});
  } finally {
    await rm(temp, {recursive: true, force: true});
  }
});

test('retained setup accepts real readonly shape and rejects changed source/check authority', () => {
  const original = savedFacts();
  const facts = retainedFacts(original);
  assert.equal(facts.patchID, retained.sourceAttempt + '/changes.patch');
  assert.equal(facts.checkID, retained.checkAttempt + '/result');
  assert.equal(facts.selectors.length, 12);
  for (const change of [
    r => { r.attempts[0].timed_out = true; },
    r => { r.attempts[0].end.result.outcome = 'blocked'; },
    r => { r.attempts[1].source.tree = 'f'.repeat(40); },
    r => { r.attempts[1].source.input_patch_id = 'unrelated/changes.patch'; },
    r => { r.frontend_check_recovery.serial = false; },
    r => { r.process.max_attempts = 65; },
    r => { r.continued_by = {run_id: 'already-transferred'}; }
  ]) {
    const changed = structuredClone(original);
    change(changed);
    assert.throws(() => retainedFacts(changed));
  }
});

test('new instance disables work caps while preserving resource policy and credential sources', () => {
  const original = {
    projects: ['project.json'], enforce_work_limits: true, concurrency: 1, max_active_attempts: 1, task_concurrency: 2,
    storage: {max_managed: '10GiB', database: {max_record: '40KiB', max_runs: 128}},
    codex: {binary: 'codex', auth_file: 'private/auth.json', go_binary: './runtime/go', go_module_cache: 'runtime/modules',
      read_only_paths: ['frontend'], command_read_paths: ['runtime/tools']},
    release: {command: ['/old/protected/operator', '--policy', '/old/release-policy.json']}
  };
  const before = structuredClone(original);
  const prepared = resolveInstance(original, '/old/coordinator/instance.json', '/new/config/projects/sudoku-preview.json');
  assert.deepEqual(original, before);
  assert.deepEqual(prepared.projects, ['/old/coordinator/project.json', '/new/config/projects/sudoku-preview.json']);
  assert.deepEqual(prepared.storage, original.storage);
  assert.deepEqual(prepared.release, original.release);
  assert.equal(prepared.concurrency, 1);
  assert.equal(prepared.task_concurrency, original.task_concurrency);
  assert.equal(prepared.max_active_attempts, 2);
  assert.equal(prepared.enforce_work_limits, false);
  assert.equal(prepared.codex.auth_file, '/old/coordinator/private/auth.json');
  assert.equal(prepared.codex.binary, 'codex');
  assert.deepEqual(prepared.codex.read_only_paths, ['frontend']);
  assert.deepEqual(prepared.codex.command_read_paths, ['/old/coordinator/runtime/tools']);
  assert.throws(() => resolveInstance({...original, concurrency: 2}, '/old/instance.json', '/new/project.json'));
});

test('setup exposes bounded adaptive roles and authenticated dependency file bindings', async () => {
  const process = JSON.parse(await readFile(path.join(path.dirname(fileURLToPath(import.meta.url)), 'process.json'), 'utf8'));
  assert.equal(process.steps.length, 1);
  assert.equal(process.max_attempts, 64);
  assert.equal(process.max_attempts_per_step, 4);
  assert.equal(process.tasks.max_concurrent, 2);
  assert.equal(process.tasks.max_tasks, 20);
  assert.equal(process.tasks.max_revisions, 0);
  assert.equal(process.steps[0].routes.ready.next, '$fail');
  const runtime = {
    source: {repository: '/old/source', base: retained.base, tree: retained.tree},
    sourceHelper: '/tools/import', checker: '/tools/check', data: '/old/state', preview: '/new/preview',
    codex: '/tools/codex', go: '/tools/go', node: '/tools/node', modules: '/runtime/node_modules', playwright: '/runtime/playwright.mjs',
    chromium: '/runtime/cft', webkit: '/runtime/webkit', agreement: '/old/inputs/agreement.json',
    frontendHelper: '/tools/frontend-check', path: '/tools:/usr/bin:/bin', goCache: '/cache/go', goModuleCache: '/cache/modules',
    nativeWritePaths: ['/cache/go'], checkWritePaths: ['/cache/go', '/new/preview'], opensslConf: '/dev/null',
    skills: ['/skills/puzzle/SKILL.md', '/skills/product/SKILL.md', '/skills/interaction/SKILL.md'],
    selectors: ['src/board.test.ts'], patchSHA256: 'a'.repeat(64), checkSHA256: retained.checkSHA256,
    exclude: retained.exclude.join(','), include: retained.include.join(',')
  };
  const roles = roleDefinitions(runtime);
  const byID = Object.fromEntries(roles.map(role => [role.id, role]));
  assert.equal(byID['sudoku-materialize'].execution.access, 'workspace-write');
  assert.deepEqual(byID['sudoku-developer'].inputs, {});
  assert.deepEqual(byID['sudoku-source-review'].inputs, {patch: 'artifact', checks: 'artifact'});
  assert.deepEqual(byID['sudoku-ux-review'].inputs, {});
  assert.deepEqual(byID['sudoku-check'].inputs, {checks: 'artifact'});
  assert.ok(byID['sudoku-check'].execution.command.args.includes('{dependency.import.artifact}'));
  assert.ok(byID['sudoku-check'].execution.command.args.includes('{artifact.checks}'));
  assert.equal(byID['sudoku-check'].execution.command.result, 'domain.json');
  assert.equal(byID['sudoku-check'].execution.command.artifact, 'evidence.tar.gz');
  assert.ok(!Object.hasOwn(byID['sudoku-check'].execution.command, 'exit_outcomes'));
  assert.equal(byID['sudoku-materialize'].execution.command.args.length, 25);
  assert.equal(byID['sudoku-check'].execution.command.args.length, 27);
  assert.equal(retained.include.length, 18);
  for (const role of roles) {
    assert.ok(Buffer.byteLength(JSON.stringify(role, null, 2) + '\n') < 16 << 10);
    assert.notEqual(role.execution.harness, 'release');
    if (role.execution.harness === 'native') {
      assert.deepEqual(role.outputs, {report: 'text'});
      assert.equal(role.execution.model, 'gpt-6.1-sol');
      assert.equal(role.execution.native.context, 'lean');
      assert.equal(role.execution.native.skills.mode, 'none');
      if (role.id !== 'sudoku-lead') {
        assert.ok(!Object.hasOwn(role.inputs, 'request'));
        assert.ok(!Object.hasOwn(role.inputs, 'feedback'));
        assert.match(role.execution.instructions, /authoritative task_context\.goal/);
      }
    }
  }
  const lead = byID['sudoku-lead'].execution.instructions;
  assert.doesNotMatch(lead, /inputs request:text|feedback:text/);
  assert.match(lead, /first visit admit only two children/);
  assert.match(lead, /choose only the necessary next tasks whose evidence and dependencies are ready/);
  assert.match(lead, /Admit code edits only for concrete product findings/);
  assert.match(lead, /Independent reviews may run in parallel when ready/);
  assert.doesNotMatch(lead, /source-N|visual-N|repair-N|check-N|source-1|visual-1|four cycles|exactly four children/);
  assert.match(lead, /brief must be at most 400 characters/);
  assert.match(lead, /acceptance must be two compact observable conditions/);
  assert.match(lead, /browser\.neighbors===3/);
  assert.match(lead, /browser\.neighbors_failed===0/);
  assert.match(lead, /two engines, 146 geometry rows and 20 journeys passed/);
  assert.match(lead, /frontend\.status passed with typecheck_passed true/);
  assert.match(lead, /cleanup\.browser_closed, server_stopped, database_removed and build_scratch_removed all true/);
  assert.match(lead, /preview\.running false/);
  assert.match(lead, /independent source and visual reports to approve that SAME source_sha256/);
  assert.match(lead, /task_context\.attempt_budget\.unlimited/);
  assert.match(lead, /task_context\.unlimited_tasks/);
  assert.match(lead, /remaining_attempts and remaining_tasks are -1/);
  assert.match(lead, /do not stop or ask for an allowance/);
  assert.match(lead, /never dump the whole patch or raw check report/);
  assert.match(lead, /delivered check:text and compact protected summary\.json through request\.evidence_files using existing native file tools/);
  assert.match(lead, /Completed\/ready with summary\.status failed or blocked is delivered failure evidence, not review readiness/);
  assert.match(lead, /Do not pre-admit reviews behind an unfinished checker/);
  assert.match(lead, /Known frontend\/build failures return actionable feedback to development before final reviews/);
  assert.match(lead, /earlier source diagnostic audit.*current source and actual failed evidence/);
  assert.match(lead, /Visual review needs inspectable current full viewport images and interaction evidence/);
  assert.match(lead, /one developer owner to repair and validate.*followed by the fixed checker/);
  assert.match(lead, /Do not attach the entire historical report chain/);
  const developer=byID['sudoku-developer'].execution.instructions;
  assert.match(developer, /smallest relevant existing tests before returning/);
  assert.ok(developer.includes(JSON.stringify(runtime.frontendHelper)));
  for(const flag of ['--tests','--serial','--source','--scratch'])assert.ok(developer.includes(flag));
  assert.match(developer, /obsolete test driver or expectation after an approved interaction change/);
  assert.match(developer, /retaining the property each assertion protects/);
  assert.match(developer, /Do not repeat passing checks without a relevant change or unresolved gap/);
  assert.match(developer, /focused developer evidence does not replace it/);
  assert.doesNotMatch(developer, /Do not run tests|change test limits\/assertions|already admitted checker/);
  const source=byID['sudoku-source-review'].execution.instructions;
  assert.match(source, /targeted relevant hunks/);
  assert.match(source, /not whole-goal or visual acceptance/);
  assert.match(source, /browser or visual gap.*does not by itself negate valid source\/frontend proof/);
  assert.match(source, /diagnostic source audit while frontend proof fails/);
  assert.match(source, /original regression property remains protected/);
  assert.match(source, /Never reinterpret an older blocked report as approval/);
  assert.match(byID['sudoku-ux-review'].execution.instructions, /Completed checker is not proof that current images or interaction evidence exist/);
  assert.match(byID['sudoku-ux-review'].execution.instructions, /do not repeat a visual audit of an empty\/test-only archive/);
  for (const id of ['shared-suguru-smoke', 'shared-nonogram-smoke', 'shared-shikaku-smoke']) {
    assert.ok(lead.includes(id));
  }
});
