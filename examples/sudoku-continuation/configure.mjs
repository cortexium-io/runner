#!/usr/bin/env node
import {constants, createReadStream} from 'node:fs';
import {access, chmod, lstat, mkdir, readFile, readdir, realpath, stat, writeFile} from 'node:fs/promises';
import {createHash} from 'node:crypto';
import {execFile} from 'node:child_process';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {parseArgs, promisify} from 'node:util';
import {roleDefinitions} from './roles.mjs';

// This is one reviewed retained-source handoff, not a general artifact import
// utility. Core independently authenticates the complete family in its transaction.
export const retained = Object.freeze({
  run: 'run_FNR3X224USDKLFCMAKXCGD754R',
  sourceAttempt: 'attempt_W5RH764QVM43P33C4F7F7QKFOU',
  checkAttempt: 'attempt_6GJSYCYUNLWGAKZA4IAI5UVJNY',
  base: 'f1dd06a18660940d27980077f88e765351e91daa',
  tree: '76e01872f8a1004daf61ef55c7fe0da7119af78f',
  checkSHA256: 'ca13f5557a375e1c25704355f9987b15af710b3d7709dddaad0b0fe3ab862efb',
  priorAttempts: 36,
  maxAttempts: 64,
  requestID: 'sudoku-native-preview-handoff-20261115-v1',
  exclude: ['docs/evidence', 'docs/benchmarks', 'docs/design', 'mobile/evidence'],
  include: [
    'docs/benchmarks/grading-v1/checksums.json',
    'docs/benchmarks/grading-v1/exchange-provenance.json',
    'docs/benchmarks/grading-v1/external-sudoku.json',
    'docs/benchmarks/grading-v1/label-expectations.json',
    'docs/benchmarks/grading-v1/manifest.json',
    'docs/benchmarks/grading-v1/original-fixtures.json',
    'docs/benchmarks/grading-v1/sources.json',
    'docs/benchmarks/profiles-v1/sdk6-e-7sk.json',
    'docs/benchmarks/profiles-v1/sdk6-e-lgdk.json',
    'docs/benchmarks/profiles-v1/sdk6-m-12ky.json',
    'docs/benchmarks/profiles-v1/sdk6-v-7ps.json',
    'docs/benchmarks/sgr4-v1/diagnostic-boards.json',
    'docs/benchmarks/sgr4-v1/diagnostic-recipes.json',
    'docs/benchmarks/sgr4-v1/freeze.json',
    'docs/benchmarks/sgr4-v1/splits.json',
    'docs/benchmarks/sgr8-qualification-v2/accepted-identities.json',
    'docs/evidence/narrated-guides-2026-11-13/media-manifest.json',
    'docs/evidence/sudoku-hold-2026-10-08/coordinator-suguru-band-review.json'
  ]
});

const here = path.dirname(fileURLToPath(import.meta.url));
const hash = data => createHash('sha256').update(data).digest('hex');
const within = (root, value) => value === root || value.startsWith(root + path.sep);
const overlaps = (a, b) => within(a, b) || within(b, a);

function absolute(value, label) {
  if (typeof value !== 'string' || !path.isAbsolute(value) || value.includes('\0')) {
    throw new Error(label + ' requires an absolute path');
  }
  return path.resolve(value);
}

async function inputFile(filename, limit) {
  const info = await lstat(filename);
  if (!info.isFile() || info.size > limit) throw new Error('not a bounded regular input file: ' + filename);
  const data = await readFile(filename);
  if (data.length > limit || data.length !== info.size) throw new Error('input changed during bounded read: ' + filename);
  return data;
}

async function directory(filename) {
  const canonical = await realpath(filename);
  if (!(await stat(canonical)).isDirectory()) throw new Error('directory required: ' + filename);
  return canonical;
}

async function fingerprint(filename, executable = false) {
  const canonical = await realpath(filename);
  const info = await stat(canonical);
  if (!info.isFile() || info.size > 512 << 20) throw new Error('runtime file exceeds its regular-file/512MiB bound: ' + filename);
  await access(canonical, executable ? constants.X_OK : constants.R_OK);
  const digest = createHash('sha256');
  for await (const chunk of createReadStream(canonical)) digest.update(chunk);
  return {path: canonical, sha256: digest.digest('hex'), bytes: info.size};
}

export async function emptyOwnedRoot(filename) {
  let current = absolute(filename, '--root');
  const missing = [];
  for (;;) {
    try {
      const info = await lstat(current);
      if (!info.isDirectory() || info.isSymbolicLink()) throw new Error('root/parent must be a real directory: ' + current);
      current = await realpath(current);
      break;
    } catch (error) {
      if (error.code !== 'ENOENT') throw error;
      missing.unshift(path.basename(current));
      current = path.dirname(current);
    }
  }
  const root = path.join(current, ...missing);
  if (root === path.parse(root).root || root === os.homedir()) throw new Error('refusing a filesystem or home root');
  try {
    const info = await lstat(root);
    if (!info.isDirectory() || info.isSymbolicLink() || process.getuid && info.uid !== process.getuid()) {
      throw new Error('private root must be an owned real directory');
    }
    if ((await readdir(root)).length) throw new Error('refusing non-empty root: ' + root);
  } catch (error) {
    if (error.code !== 'ENOENT') throw error;
  }
  return root;
}

function profileFor(role, selected) {
  if (role?.execution && (!selected || selected === 'default')) return role.execution;
  return role?.profiles?.[selected || role.default_profile];
}

export function retainedFacts(inspect) {
  if (inspect.id !== retained.run || inspect.status !== 'Failed' || inspect.process?.id !== 'deliver-planned-frontend' ||
      inspect.process.max_attempts !== retained.maxAttempts || !Number.isSafeInteger(inspect.revision) || inspect.revision < 1 ||
      !inspect.resources_released || inspect.cleanup_error || inspect.cleanup_blocked || inspect.cancel_requested || inspect.decision || inspect.continued_by ||
      !Array.isArray(inspect.attempts) || inspect.attempts.some(attempt => attempt.state !== 'finished' || !attempt.released)) {
    throw new Error('inspect is not the exact settled, released, untransferred retained failed root');
  }
  const source = inspect.attempts.find(attempt => attempt.id === retained.sourceAttempt);
  const checked = inspect.attempts.find(attempt => attempt.id === retained.checkAttempt);
  const patchID = retained.sourceAttempt + '/changes.patch';
  const checkID = retained.checkAttempt + '/result';
  const grant = inspect.frontend_check_recovery;
  if (!source || source.superseded || source.timed_out || source.stop_requested || source.end?.kind !== 'returned' || source.end.retryable || source.end.result?.outcome !== 'ready' ||
      source.source?.attempt_id !== source.id || source.source.patch_id !== patchID || source.source.base !== retained.base || source.source.tree !== retained.tree ||
      source.end.result.outputs?.patch?.artifact_id !== patchID || source.source.repository !== inspect.repository ||
      !checked || checked.superseded || checked.timed_out || checked.stop_requested || checked.end?.kind !== 'returned' || checked.end.retryable || checked.end.result?.outcome !== 'ready' ||
      checked.end.result.outputs?.artifact?.artifact_id !== checkID || checked.source?.attempt_id !== checked.id || checked.source.input_patch_id !== patchID ||
      checked.source.base !== retained.base || checked.source.repository !== source.source.repository || checked.source.work_dir !== source.source.work_dir ||
      checked.source.tree && checked.source.tree !== retained.tree ||
      !grant?.serial || !grant.request_id || grant.patch_id !== patchID || !/^[a-f0-9]{64}$/.test(grant.patch_sha256 || '') ||
      !/^[a-f0-9]{64}$/.test(grant.helper_sha256 || '') || checked.visit < grant.check_visit || inspect.process.steps?.[checked.step]?.id !== grant.check_step) {
    throw new Error('retained candidate, passing serial check and owner grant identities differ');
  }
  const input = inspect.inputs?.tests?.inline;
  const selectors = typeof input === 'string' ? JSON.parse(input) : input;
  if (!Array.isArray(selectors) || selectors.length !== 12 || new Set(selectors).size !== selectors.length ||
      selectors.some(name => typeof name !== 'string' || !name.endsWith('.test.ts') || name.startsWith('/') || name.split('/').includes('..'))) {
    throw new Error('the exact twelve original frontend selectors are required');
  }
  const checkRole = inspect.process.steps[checked.step].role;
  const checkProfile = profileFor(inspect.roles?.[checkRole], checked.profile || inspect.profiles?.[checkRole]);
  if (checkProfile?.harness !== 'command' || !checkProfile.command?.args?.length) throw new Error('saved fixed frontend checker profile is missing');
  return {source: source.source, patchID, checkID, patchSHA256: grant.patch_sha256, helperSHA256: grant.helper_sha256,
    selectors, checkProfile, serialGrant: grant.request_id, revision: inspect.revision, ownAttempts: inspect.attempts.length};
}

export function resolveInstance(instance, instancePath, targetProject) {
  const result = structuredClone(instance);
  if (!Array.isArray(result.projects) || result.projects.length < 1 || result.projects.length >= 16 || result.concurrency !== 1) {
    throw new Error('old instance must retain concurrency one and leave one configured project slot');
  }
  result.projects = result.projects.map(filename => path.resolve(path.dirname(instancePath), filename));
  result.projects.push(targetProject);
  if (result.codex) {
    for (const key of ['binary', 'go_binary']) {
      if (result.codex[key]?.includes(path.sep)) result.codex[key] = path.resolve(path.dirname(instancePath), result.codex[key]);
    }
    for (const key of ['auth_file', 'go_module_cache', 'go_check_binary']) {
      if (result.codex[key]) result.codex[key] = path.resolve(path.dirname(instancePath), result.codex[key]);
    }
    if (result.codex.command_read_paths) result.codex.command_read_paths = result.codex.command_read_paths.map(filename => path.resolve(path.dirname(instancePath), filename));
  }
  // Explicitly authorized machine admission for the new general-task family.
  // Legacy task-source concurrency and every stored run definition stay frozen.
  result.max_active_attempts = 2;
  return result;
}

function cacheHome() {
  if (process.platform === 'darwin') return path.join(os.homedir(), 'Library', 'Caches');
  if (process.platform === 'win32') return absolute(process.env.LOCALAPPDATA, 'LOCALAPPDATA');
  return absolute(process.env.XDG_CACHE_HOME || path.join(os.homedir(), '.cache'), 'user cache');
}

async function jsonFile(filename, value, limit = 16 << 10) {
  const data = JSON.stringify(value, null, 2) + '\n';
  if (Buffer.byteLength(data) > limit) throw new Error('generated file exceeds its byte bound: ' + filename);
  await writeFile(filename, data, {mode: 0o600, flag: 'wx'});
}

export async function configure(values) {
  const root = await emptyOwnedRoot(values.root);
  for (const key of ['instance', 'inspect', 'data', 'agreement', 'source-helper', 'checker', 'codex', 'go', 'node', 'modules', 'playwright', 'chromium', 'webkit']) {
    absolute(values[key], '--' + key);
  }
  const data = await directory(values.data);
  if (overlaps(root, data)) throw new Error('new configuration/source/preview must be outside existing coordinator state');
  const instancePath = await realpath(values.instance);
  const instanceBytes = await inputFile(instancePath, 16 << 10);
  const instance = JSON.parse(instanceBytes);
  const inspectBytes = await inputFile(values.inspect, 16 << 20);
  const inspect = JSON.parse(inspectBytes);
  const facts = retainedFacts(inspect);
  const repository = await directory(facts.source.repository);
  if (overlaps(repository, root)) throw new Error('new root overlaps the retained source repository');
  for (const projectFile of instance.projects || []) {
    const project = JSON.parse(await inputFile(path.resolve(path.dirname(instancePath), projectFile), 16 << 10));
    if (project.id === 'sudoku-preview') throw new Error('sudoku-preview is already configured; preserve the existing continuation');
  }
  const canonicalArtifact = async id => {
    if (!/^[A-Za-z0-9_-]+\/[A-Za-z0-9_.-]+$/.test(id)) throw new Error('invalid canonical artifact identity');
    const filename = path.join(data, 'artifacts', id);
    const bytes = await inputFile(filename, 16 << 20);
    const canonical = await realpath(filename);
    if (!within(path.join(data, 'artifacts'), canonical)) throw new Error('artifact escapes the existing state artifact directory');
    return {path: canonical, sha256: hash(bytes), bytes};
  };
  const patch = await canonicalArtifact(facts.patchID);
  const checks = await canonicalArtifact(facts.checkID);
  if (patch.sha256 !== facts.patchSHA256 || checks.sha256 !== retained.checkSHA256) throw new Error('retained patch/check SHA256 differs from the authenticated handoff');
  const prior = JSON.parse(checks.bytes);
  if (prior.schema !== 2 || prior.status !== 'passed' || prior.executed !== 393 || prior.failed !== 0 || prior.skipped !== 0 || prior.vitest_incomplete ||
      prior.test_workers !== 1 || prior.test_timeout_ms !== 5000 || prior.typecheck?.status !== 'passed' || !prior.typecheck.complete || prior.typecheck.exit_code !== 0 ||
      !Array.isArray(prior.tests) || facts.selectors.some(name => !prior.tests.includes(name))) {
    throw new Error('canonical prior artifact does not contain complete matching 393-pass serial/typecheck proof');
  }
  const agreement = await realpath(values.agreement);
  const agreementBytes = await inputFile(agreement, 128 << 10);
  JSON.parse(agreementBytes); // Preserve the complete self-contained agreement, not one excerpt.
  const requestText = agreementBytes.toString('utf8');
  const selected = {};
  const identities = {};
  for (const key of ['source-helper', 'checker', 'codex', 'go', 'node', 'chromium', 'webkit']) {
    identities[key] = await fingerprint(values[key], true);
    selected[key] = identities[key].path;
  }
  identities.playwright = await fingerprint(values.playwright);
  selected.playwright = identities.playwright.path;
  selected.modules = await directory(values.modules);
  const helperPath = values['frontend-helper'] || facts.checkProfile.command.args[0];
  absolute(helperPath, '--frontend-helper or saved checker executable');
  identities['frontend-helper'] = await fingerprint(helperPath, true);
  if (identities['frontend-helper'].sha256 !== facts.helperSHA256) throw new Error('fixed frontend helper differs from the serial owner grant');
  const skills = [];
  for (const [key, id] of [['puzzle-skill', 'puzzle-experience-review'], ['product-skill', 'product-experience-review'], ['interaction-skill', 'runner-interaction-design']]) {
    const filename = values[key] || path.join(os.homedir(), '.codex', 'skills', id, 'SKILL.md');
    absolute(filename, '--' + key);
    identities[key] = await fingerprint(filename);
    skills.push(identities[key].path);
  }
  const {stdout} = await promisify(execFile)(selected.go, ['env', '-json', 'GOCACHE', 'GOMODCACHE', 'GOVERSION'], {
    cwd: here, env: {...process.env, GOTOOLCHAIN: 'local'}, maxBuffer: 64 << 10
  });
  const goEnvironment = JSON.parse(stdout);
  const goCache = await directory(absolute(goEnvironment.GOCACHE, 'selected Go GOCACHE'));
  const oldModuleCache = instance.codex?.go_module_cache && path.resolve(path.dirname(instancePath), instance.codex.go_module_cache);
  const goModuleCache = await directory(absolute(oldModuleCache || facts.checkProfile.command.env?.RUNNER_GO_MODULES || goEnvironment.GOMODCACHE, 'selected qualified Go module cache'));
  const maintenance = await directory(absolute(values['maintenance-locks'] || path.join(cacheHome(), 'codex-maintenance'), 'maintenance lock directory'));
  const nativeWritePaths = [...new Set([goCache, goModuleCache, maintenance])];
  const broad = new Set([path.parse(root).root, os.homedir(), cacheHome()]);
  for (const filename of nativeWritePaths) {
    if (broad.has(filename) || overlaps(filename, data) || overlaps(filename, root) || overlaps(filename, repository)) throw new Error('refusing broad/coordinator/source runtime write root: ' + filename);
    await access(filename, constants.R_OK | constants.W_OK);
  }
  const source = path.join(root, 'source');
  const preview = path.join(root, 'preview');
  const config = path.join(root, 'config');
  const projectFile = path.join(config, 'projects', 'sudoku-preview.json');
  const runtime = {
    root, data, source: {...facts.source, repository}, workspace: source, preview, agreement,
    codex: selected.codex, go: selected.go, node: selected.node, modules: selected.modules,
    playwright: selected.playwright, chromium: selected.chromium, webkit: selected.webkit,
    sourceHelper: selected['source-helper'], checker: selected.checker,
    frontendHelper: identities['frontend-helper'].path,
    goCache, goModuleCache, nativeWritePaths, checkWritePaths: [...nativeWritePaths, preview],
    opensslConf: facts.checkProfile.command.env?.OPENSSL_CONF || '/dev/null',
    path: [...new Set([path.dirname(selected.go), path.dirname(selected.node), path.dirname(selected.codex), '/usr/bin', '/bin', '/usr/sbin', '/sbin'])].join(path.delimiter),
    skills, selectors: facts.selectors, patchSHA256: patch.sha256, checkSHA256: checks.sha256,
    exclude: retained.exclude.join(','), include: retained.include.join(',')
  };
  const roles = roleDefinitions(runtime);
  const processDefinition = JSON.parse(await inputFile(path.join(here, 'process.json'), 16 << 10));
  const resolvedInstance = resolveInstance(instance, instancePath, projectFile);
  const continued = {
    request_id: retained.requestID, run_id: retained.run, revision: facts.revision,
    project: 'sudoku-preview', process: processDefinition.id,
    source_attempt_id: retained.sourceAttempt, patch_sha256: patch.sha256,
    check_attempt_id: retained.checkAttempt, check_sha256: checks.sha256,
    inputs: {request: requestText}, artifacts: {patch: facts.patchID, checks: facts.checkID}
  };
  const documents = [
    [path.join(config, 'instance.json'), resolvedInstance],
    [projectFile, {id: 'sudoku-preview', name: 'Audited Sudoku native-role preview continuation', workspace: source,
      processes: {[processDefinition.id]: '../processes/continue-sudoku-preview.json'}, default_process: processDefinition.id,
      roles: Object.fromEntries(roles.map(role => [role.id, {file: '../roles/' + role.id + '.json'}]))}],
    [path.join(config, 'processes', 'continue-sudoku-preview.json'), processDefinition],
    ...roles.map(role => [path.join(config, 'roles', role.id + '.json'), role])
  ];
  // Validate all bounded config documents before creating any owned output.
  for (const [filename, value] of documents) {
    if (Buffer.byteLength(JSON.stringify(value, null, 2) + '\n') > 16 << 10) throw new Error('configuration exceeds the loader 16KiB bound: ' + filename);
  }
  for (const dirname of [root, source, preview, config, path.join(config, 'projects'), path.join(config, 'processes'), path.join(config, 'roles'), path.join(root, 'requests')]) {
    await mkdir(dirname, {recursive: true, mode: 0o700});
  }
  await chmod(root, 0o700);
  for (const [filename, value] of documents) await jsonFile(filename, value);
  await jsonFile(path.join(root, 'requests', 'continue.json'), continued, 256 << 10);
  await jsonFile(path.join(root, 'runtime.json'), {
    schema: 1, prepared_at: new Date().toISOString(), machine: os.hostname(), platform: processPlatform(),
    original_run: retained.run, expected_prior_attempts: retained.priorAttempts, original_total_attempts: retained.maxAttempts,
    observed_root_attempts: facts.ownAttempts, observed_family_revision: facts.revision,
    authority: 'Core.ContinueTask revalidates all original family records, source capture and canonical evidence, atomically freezes original spending and charges the authoritative historical debit. This builder grants or submits nothing.',
    original_instance: {path: instancePath, sha256: hash(instanceBytes)}, original_inspect: {path: await realpath(values.inspect), sha256: hash(inspectBytes)},
    original_agreement: {path: agreement, sha256: hash(agreementBytes), bytes: agreementBytes.length},
    source: {repository, base: retained.base, full_tree: retained.tree, patch: {id: facts.patchID, path: patch.path, sha256: patch.sha256},
      checks: {id: facts.checkID, path: checks.path, sha256: checks.sha256}, serial_grant: facts.serialGrant,
      excluded_unchanged_history: retained.exclude, included_imported_json: retained.include,
      scope: 'The fixed importer verifies the complete base+patch tree before materializing the bounded source projection. Historical directories stay canonical at origin; no old bytes are deleted.'},
    workspace: source, preview, existing_state: data, config: path.join(config, 'instance.json'),
    admission: {concurrency: resolvedInstance.concurrency, old_max_active_attempts: instance.max_active_attempts ?? 0, max_active_attempts: 2,
      reason: 'Explicit new machine admission for two eligible general-task reviewers; original legacy concurrency and every stored process/attempt ceiling remain unchanged.'},
    runtime: {...selected, go_version: goEnvironment.GOVERSION, go_cache: goCache, go_module_cache: goModuleCache,
      maintenance_locks: maintenance, native_write_paths: nativeWritePaths, checker_write_paths: runtime.checkWritePaths, identities},
    profiles: Object.fromEntries(roles.map(role => [role.id, {harness: role.execution.harness, access: role.execution.access,
      mode: role.execution.mode, model: role.execution.model, reasoning: role.execution.reasoning,
      context: role.execution.native?.context, tools: role.execution.native?.tools, skills: role.execution.native?.skills,
      max_input: role.execution.max_input, max_output: role.execution.max_output}])),
    native_provenance: 'Runner retains actual harness/tool/skill exposure, observed use, usage and cleanup facts per attempt; these setup fingerprints do not prove future execution.',
    request_file: path.join(root, 'requests', 'continue.json')
  }, 128 << 10);
  return {config: path.join(config, 'instance.json'), request: path.join(root, 'requests', 'continue.json'),
    runtime: path.join(root, 'runtime.json'), data, project: 'sudoku-preview', workspace: source, preview,
    request_id: retained.requestID, expected_prior_attempts: retained.priorAttempts, total_attempts: retained.maxAttempts,
    max_active_attempts: 2, submitted: false};
}

function processPlatform() { return {os: os.platform(), release: os.release(), arch: os.arch(), node: process.version}; }

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const options = Object.fromEntries([
    'root', 'instance', 'inspect', 'data', 'agreement', 'source-helper', 'checker', 'codex', 'go', 'modules', 'playwright', 'chromium', 'webkit',
    'frontend-helper', 'maintenance-locks', 'puzzle-skill', 'product-skill', 'interaction-skill'
  ].map(name => [name, {type: 'string'}]));
  options.node = {type: 'string', default: process.execPath};
  try {
    const {values} = parseArgs({options});
    const result = await configure(values);
    process.stdout.write(JSON.stringify(result, null, 2) + '\n');
  } catch (error) {
    process.stderr.write(error.message + '\n');
    process.exitCode = 1;
  }
}
