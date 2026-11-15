#!/usr/bin/env node
import {constants, createReadStream} from 'node:fs';
import {access, mkdir, readFile, readdir, stat, writeFile} from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import {fileURLToPath} from 'node:url';
import {createHash, randomBytes, randomUUID} from 'node:crypto';
import {execFile} from 'node:child_process';
import {parseArgs, promisify} from 'node:util';

const {values} = parseArgs({
  options: {
    root: {type: 'string'},
    'verify-existing': {type: 'string'},
    'expected-binary-sha256': {type: 'string'},
    'app-workspace': {type: 'string'},
    'project-name': {type: 'string', default: 'Campaign Studio'},
    module: {type: 'string'},
    'native-context': {type: 'string', default: 'lean'},
    'genny-skill': {type: 'string', default: path.join(os.homedir(), '.codex', 'skills', 'genny-cli', 'SKILL.md')},
    codex: {type: 'string'},
    genny: {type: 'string'},
    go: {type: 'string'},
    npm: {type: 'string'},
    browser: {type: 'string'},
    playwright: {type: 'string'},
    node: {type: 'string', default: process.execPath},
    'critic-harness': {type: 'string', default: 'codex'},
    'critic-model': {type: 'string'},
    claude: {type: 'string'}
  }
});
const here = path.dirname(fileURLToPath(import.meta.url));
const repo = path.resolve(here, '../..');
if (!['lean', 'native'].includes(values['native-context'])) throw new Error('--native-context must be lean or native');
let sourceRuntime;
if (values['verify-existing']) {
  if (!path.isAbsolute(values['verify-existing'])) throw new Error('--verify-existing requires an absolute prior qualification root');
  sourceRuntime = JSON.parse(await readFile(path.join(values['verify-existing'], 'runtime.json'), 'utf8'));
  for (const key of ['codex', 'genny', 'go', 'npm', 'browser', 'playwright']) {
    if (!values[key]) values[key] = sourceRuntime[key];
  }
  if (!values['app-workspace']) values['app-workspace'] = sourceRuntime.app;
  if (path.resolve(values['app-workspace']) !== path.resolve(sourceRuntime.app)) {
    throw new Error('verification must reference the original runtime app workspace');
  }
  if (!/^[a-f0-9]{64}$/i.test(values['expected-binary-sha256'] || '')) {
    throw new Error('--verify-existing requires the accepted --expected-binary-sha256');
  }
}
const required = ['root', 'app-workspace', 'genny', 'go', 'npm', 'browser', 'playwright', 'node'];
if (!sourceRuntime) required.push('codex');
for (const key of required) {
  if (!values[key] || !path.isAbsolute(values[key])) {
    throw new Error('--' + key + ' requires an absolute path');
  }
}
if (!['codex', 'claude'].includes(values['critic-harness'])) {
  throw new Error('--critic-harness must be codex or claude');
}
if (values['critic-harness'] === 'claude' && (!values.claude || !path.isAbsolute(values.claude))) {
  throw new Error('--claude requires an absolute executable for the optional Claude critic');
}
const executables = ['genny', 'go', 'npm', 'browser', 'node'];
if (!sourceRuntime) executables.push('codex');
for (const key of executables) {
  await access(values[key], constants.X_OK);
}
await access(values.playwright, constants.R_OK);
if (values.claude) await access(values.claude, constants.X_OK);
const {stdout: goEnvironment} = await promisify(execFile)(values.go, ['env', 'GOCACHE', 'GOMODCACHE'], {
  cwd: here,
  env: {...process.env, GOTOOLCHAIN: 'local'},
  maxBuffer: 64 << 10
});
const cacheRoots = goEnvironment.trim().split(/\r?\n/);
if (cacheRoots.length !== 2 || cacheRoots.some(directory => !path.isAbsolute(directory))) {
  throw new Error('selected Go did not report two absolute shared cache roots');
}
// Follow Go os.UserCacheDir for the platforms supported by this Node helper.
function userCacheDirectory() {
  let directory;
  if (process.platform === 'darwin') {
    if (process.env.HOME) directory = path.join(process.env.HOME, 'Library', 'Caches');
  } else if (process.platform === 'win32') {
    directory = process.env.LocalAppData;
  } else {
    directory = process.env.XDG_CACHE_HOME;
    if (!directory && process.env.HOME) directory = path.join(process.env.HOME, '.cache');
  }
  if (!directory || !path.isAbsolute(directory)) {
    throw new Error('cannot resolve the absolute Go os.UserCacheDir for this host');
  }
  return path.resolve(directory);
}
const userCache = userCacheDirectory();
const gennyProjectLocks = path.join(userCache, 'genny', 'project-locks');
const maintenanceLocks = path.join(userCache, 'codex-maintenance');
const {stdout: npmEnvironment} = await promisify(execFile)(values.npm, ['config', 'get', 'cache'], {
  cwd: here,
  env: {...process.env, PATH: path.dirname(values.node) + path.delimiter + process.env.PATH},
  maxBuffer: 64 << 10
});
const npmCache = npmEnvironment.trim();
if (!path.isAbsolute(npmCache) || npmCache.includes('\n')) {
  throw new Error('selected npm did not report one absolute cache directory');
}
const runtimeWritePaths = [...new Set([...cacheRoots, gennyProjectLocks, npmCache, maintenanceLocks].map(directory => path.resolve(directory)))];
const broadDirectories = new Set([path.parse(here).root, userCache]);
if (process.env.HOME) broadDirectories.add(path.resolve(process.env.HOME));
if (runtimeWritePaths.some(directory => broadDirectories.has(directory))) {
  throw new Error('refusing filesystem-root, whole-home or whole-user-cache write access');
}

const root = path.resolve(values.root);
const app = path.resolve(values['app-workspace']);
if (!values['project-name'].trim() || values['project-name'].length > 80) throw new Error('--project-name must be 1–80 characters');
const modulePath = values.module || 'runner.example/' + path.basename(app);
if (!/^[a-z0-9.-]+\/[a-z0-9/-]+$/.test(modulePath)) throw new Error('--module must be a simple Go module path');
if (!sourceRuntime) {
  if (!path.isAbsolute(values['genny-skill'])) throw new Error('--genny-skill must be absolute');
  await access(values['genny-skill'], constants.R_OK);
}
if (root === app || root.startsWith(app + path.sep)) {
  throw new Error('qualification configuration must be outside the app workspace');
}
async function assertEmpty(directory) {
  let entries;
  try {
    entries = await readdir(directory);
  } catch (error) {
    if (error.code === 'ENOENT') return;
    throw error;
  }
  if (entries.length) throw new Error('refusing non-empty directory: ' + directory);
}
await assertEmpty(root);
let acceptedBinarySHA256;
if (sourceRuntime) {
  await access(path.join(app, '.genny', 'genny.json'), constants.R_OK);
  await access(path.join(app, 'go.mod'), constants.R_OK);
  const binary = path.join(app, '.qualification', 'server');
  if (!(await stat(binary)).isFile()) throw new Error('accepted server must be a regular file');
  await access(binary, constants.X_OK);
  const hash = createHash('sha256');
  for await (const chunk of createReadStream(binary)) hash.update(chunk);
  acceptedBinarySHA256 = hash.digest('hex');
  if (acceptedBinarySHA256 !== values['expected-binary-sha256'].toLowerCase()) {
    throw new Error('existing app binary differs from the accepted qualification identity');
  }
  if (!path.isAbsolute(sourceRuntime.credentials)) throw new Error('original credentials reference must be absolute');
  await access(sourceRuntime.credentials, constants.R_OK);
} else {
  await assertEmpty(app);
}
for (const directory of runtimeWritePaths) {
  await mkdir(directory, {recursive: true, mode: 0o700});
  await access(directory, constants.R_OK | constants.W_OK);
}

const config = path.join(root, 'config');
const credentials = sourceRuntime ? sourceRuntime.credentials : path.join(root, 'private', 'credentials.json');
const checker = path.join(repo, 'testdata', 'role-tasks', 'qualify-saas.mjs');
const acceptance = path.join(repo, 'testdata', 'role-tasks', 'saas-acceptance.md');
const directories = [root, config, path.join(config, 'roles'), path.join(config, 'processes'),
  path.join(config, 'projects'), path.join(root, 'requests')];
if (!sourceRuntime) directories.push(app, path.join(root, 'private'), path.join(root, 'writing'));
for (const directory of directories) {
  await mkdir(directory, {recursive: true, mode: 0o700});
}
async function json(file, value, mode = 0o600) {
  await writeFile(file, JSON.stringify(value, null, 2) + '\n', {mode, flag: 'wx'});
}
if (!sourceRuntime) {
  const tenantA = randomUUID();
  const tenantB = randomUUID();
  const password = () => 'Runner-' + randomBytes(24).toString('base64url') + '-7!';
  await json(credentials, {
    tenants: [{id: tenantA, name: 'Studio A'}, {id: tenantB, name: 'Studio B'}],
    users: {
      adminA: {email: 'admin-a@runner.example.test', password: password(), tenant_id: tenantA, admin: true},
      memberA: {email: 'member-a@runner.example.test', password: password(), tenant_id: tenantA, admin: false},
      adminB: {email: 'admin-b@runner.example.test', password: password(), tenant_id: tenantB, admin: true}
    },
    totp_secret_key: randomBytes(32).toString('base64')
  });
}
const runtime = {
  app,
  project_name: sourceRuntime?.project_name || values['project-name'],
  module: sourceRuntime?.module || modulePath,
  native_context: sourceRuntime ? 'none' : values['native-context'],
  genny_skill: values['genny-skill'],
  codex: values.codex,
  genny: values.genny,
  go: values.go,
  npm: values.npm,
  node: values.node,
  browser: values.browser,
  playwright: values.playwright,
  credentials,
  checker,
  acceptance,
  shared_go_cache_roots: cacheRoots,
  genny_project_locks: gennyProjectLocks,
  npm_cache: npmCache,
  maintenance_lock_directory: maintenanceLocks,
  runtime_write_paths: runtimeWritePaths
};
if (sourceRuntime) {
  runtime.source_runtime = path.join(path.resolve(values['verify-existing']), 'runtime.json');
  runtime.accepted_binary_sha256 = acceptedBinarySHA256;
}
function roleContext(id) {
  const selected = {app, project_name: runtime.project_name, binary: '.qualification/server'};
  if (id === 'saas-lead' || id === 'saas-planner') {
    return '\nSelected project: ' + JSON.stringify(selected) + '. No publication or deployment.';
  }
  Object.assign(selected, {
    module: runtime.module,
    genny: values.genny,
    genny_version: '0.4.1',
    go: values.go,
    go_version: '1.27.4',
    node: values.node,
    npm: values.npm,
    genny_skill: values['genny-skill'],
    path_prefix: [...new Set([path.dirname(values.go), path.dirname(values.node), path.dirname(values.npm)])].join(path.delimiter)
  });
  return '\nSelected runtime: ' + JSON.stringify(selected) +
    '. Prepend path_prefix to PATH and set GOTOOLCHAIN=local for Go/Genny commands. ' +
    'Use configured shared cache roots; no copied workspace/cache. Read the named Genny skill directly even with skills:none. ' +
    'Use Go for backend/experiment tooling and JavaScript/TypeScript for frontend/browser helpers; do not introduce Python source, packages, environments or caches. ' +
    'No deployment, publication, real credentials or external messages.';
}
const roleProfiles = {};
const roleFiles = (await readdir(path.join(here, 'roles')))
  .filter(name => name.endsWith('.json') && (name !== 'writing-quick.json' || values.claude))
  .filter(name => sourceRuntime
    ? name === 'saas-fixed-check.json'
    : true).sort();
for (const name of roleFiles) {
  const role = JSON.parse(await readFile(path.join(here, 'roles', name), 'utf8'));
  if (role.execution.harness === 'native') {
    role.execution.native.binary = role.id === 'writing-quick' ? values.claude : values.codex;
    if (role.id.startsWith('saas-')) {
      role.execution.instructions += roleContext(role.id);
      role.execution.native.context = values['native-context'];
      role.execution.native.skills = {mode: values['native-context'] === 'lean' ? 'none' : 'native'};
      role.execution.native.runtime_write_paths = runtimeWritePaths;
    }
  } else if (role.id === 'saas-fixed-check') {
    if (sourceRuntime) role.execution.command.exit_outcomes = {'1': 'rejected'};
    role.execution.command.args = [
      values.node, checker, '--app', '{workspace}', '--genny', values.genny,
      '--go', values.go, '--browser', values.browser, '--playwright', values.playwright,
      '--credentials', credentials, '--evidence-dir', '{scratch}'
    ];
    if (acceptedBinarySHA256) {
      role.execution.command.args.push('--expected-binary-sha256', acceptedBinarySHA256);
    }
    role.execution.command.env = {
      PATH: [...new Set([
        path.dirname(values.go), path.dirname(values.node), path.dirname(values.npm),
        path.dirname(values.genny), '/usr/bin', '/bin', '/usr/sbin', '/sbin'
      ])].join(path.delimiter),
      GOTOOLCHAIN: 'local',
      GOCACHE: cacheRoots[0],
      GOMODCACHE: cacheRoots[1]
    };
    role.execution.command.runtime_write_paths = runtimeWritePaths;
  }
  if (role.id === 'writing-challenger' && values['critic-harness'] === 'claude') {
    role.execution.model = values['critic-model'] || 'opus';
    role.execution.reasoning = 'high';
    role.execution.native = {
      harness: 'claude',
      binary: values.claude,
      tools: {mode: 'selected', names: ['Read', 'Glob', 'Grep', 'WebFetch', 'WebSearch']},
      skills: {mode: 'none'},
      network: true
    };
  } else if (role.id === 'writing-challenger' && values['critic-model']) {
    role.execution.model = values['critic-model'];
  }
  roleProfiles[role.id] = {
    harness: role.execution.harness,
    model: role.execution.model,
    reasoning: role.execution.reasoning,
    context: role.execution.native?.context,
    tools: role.execution.native?.tools,
    skills: role.execution.native?.skills,
    max_input: role.execution.max_input,
    max_output: role.execution.max_output
  };
  await json(path.join(config, 'roles', name), role);
}
for (const name of sourceRuntime ? ['verify-existing'] : ['writing', 'saas']) {
  const projectID = name === 'writing' ? 'writing' : 'saas';
  const process = JSON.parse(await readFile(path.join(here, 'processes', name + '.json'), 'utf8'));
  await json(path.join(config, 'processes', name + '.json'), process);
  const processes = {[process.id]: '../processes/' + name + '.json'};
  if (name === 'writing' && values.claude) {
    const quick = JSON.parse(await readFile(path.join(here, 'processes', 'writing-quick.json'), 'utf8'));
    await json(path.join(config, 'processes', 'writing-quick.json'), quick);
    processes[quick.id] = '../processes/writing-quick.json';
  }
  const ids = roleFiles.map(file => file.slice(0, -5)).filter(id => id.startsWith(projectID === 'writing' ? 'writing-' : 'saas-'));
  await json(path.join(config, 'projects', projectID + '.json'), {
    id: projectID,
    name: sourceRuntime ? 'Campaign Studio verification continuation'
      : name === 'writing' ? 'Role-task writing qualification' : 'Campaign Studio SaaS qualification',
    workspace: name === 'writing' ? path.join(root, 'writing') : app,
    processes,
    default_process: process.id,
    roles: Object.fromEntries(ids.map(id => [id, {file: '../roles/' + id + '.json'}]))
  });
}
await json(path.join(config, 'instance.json'), {
  projects: sourceRuntime ? ['projects/saas.json'] : ['projects/writing.json', 'projects/saas.json'],
  concurrency: sourceRuntime ? 1 : 3,
  task_concurrency: sourceRuntime ? 1 : 2,
  reconcile_interval: '1s',
  storage: {
    max_managed: '2GiB',
    min_free: '2GiB',
    database: {max_size: '128MiB', max_record: '256KiB', max_runs: 200},
    max_artifact: '32MiB',
    keep_runs: '168h',
    logs: {max_per_attempt: '128KiB', keep_for: '48h'}
  }
});
const contract = await readFile(acceptance, 'utf8');
if (sourceRuntime) {
  await json(path.join(root, 'requests', 'verify-existing.json'), {
    request: 'Independently verify the existing Runner-built Campaign Studio against the fixed goal below. ' +
      'This is verification-only continuation: do not initialize, edit, rebuild, delete or deploy app source. ' +
      'The accepted executable SHA256 is ' + acceptedBinarySHA256 + '. ' +
      'Execute the configured fixed API/UI assertions once, with no model turn, delegation or automatic retry. ' +
      'Passing assertions complete this verification goal; failed assertions reject it while retaining the report and artifact. ' +
      'Earlier API-only evidence does not establish UI acceptance. ' +
      'Reference the original private credentials by path; do not copy or print them.\n\n' +
      contract
  });
} else {
  await json(path.join(root, 'requests', 'saas.json'), {
    request: 'Create ' + runtime.project_name + ', a useful campaign-management SaaS for small teams, from this description in the NEW assigned workspace. ' +
      'A tenant member signs in, creates and edits drafts; a tenant admin publishes a campaign; anyone can view its published preview. ' +
      'Different tenants must never see or change one another’s private campaigns. Use Genny to create a fresh ordinary Go/Vue app. ' +
      'Do not copy an older qualification project or reuse its executable. ' +
      'Use nested Runner role-task planning and actually concurrent disjoint backend/frontend work. ' +
      'Deliver the local product and independent fixed API/UI evidence in this one Runner run, including at most two autonomous repair/recheck pairs if needed. ' +
      'Make routine implementation decisions without operator steering; preserve the fixed acceptance. Do not deploy or publish.\n\n' +
      contract
  });
  await json(path.join(root, 'requests', 'writing-small.json'), {
    request: 'Write a clear 100–150 word note explaining roles, tasks and why a simple writing request should ' +
      'not need an entire software-delivery workflow. This is general explanatory copy, not a first-person ' +
      'claim from Dan. Deliver the usable note directly; do not publish it.'
  });
  await json(path.join(root, 'requests', 'writing-deliberation.json'), {
    request: 'Draft a 600–900 word blog post in Dan Larsen\'s voice about what the Pi coding agent teaches us ' +
      'about building a useful agent harness. Use https://github.com/badlogic/pi-mono/tree/main/packages/coding-agent ' +
      'as a starting primary source and verify current claims, including whether a 1.0 release actually exists; ' +
      'do not invent a release or claim this qualification proves a production system. Explain one concrete ' +
      'design tradeoff, relate it to handling both a small writing task and a multi-role coding project, ' +
      'and include source links. Use an author, then two independent concurrent critiques with different ' +
      'perspectives, consider their disagreement and make a final editorial decision. Read the writing ' +
      'voice skill. Keep it a draft and do not publish.'
  });
}
runtime.role_profiles = roleProfiles;
await json(path.join(root, 'runtime.json'), runtime);
process.stdout.write(JSON.stringify({
  config: path.join(config, 'instance.json'),
  data: path.join(root, 'state'),
  requests: path.join(root, 'requests'),
  app,
  runtime: path.join(root, 'runtime.json'),
  critic_harness: values['critic-harness'],
  mode: sourceRuntime ? 'verify-existing' : 'new-project'
}, null, 2) + '\n');
