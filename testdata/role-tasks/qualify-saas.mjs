#!/usr/bin/env node
import assert from 'node:assert/strict';
import {createReadStream} from 'node:fs';
import {mkdtemp, mkdir, readFile, rm, writeFile} from 'node:fs/promises';
import {createHash, randomUUID} from 'node:crypto';
import {spawn} from 'node:child_process';
import {once} from 'node:events';
import {createServer} from 'node:net';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath, pathToFileURL} from 'node:url';
import {parseArgs} from 'node:util';
import {checkAPI} from './api-checks.mjs';
import {openCampaignEditor} from './navigation-checks.mjs';

const {values} = parseArgs({options: Object.fromEntries([
  'app', 'genny', 'go', 'browser', 'playwright', 'credentials', 'evidence-dir', 'expected-binary-sha256'
].map(name => [name, {type: 'string'}]))});
for (const [name, value] of Object.entries(values)) {
  if (name !== 'expected-binary-sha256' && !path.isAbsolute(value)) throw new Error('--' + name + ' must be absolute');
}
for (const name of ['app', 'genny', 'go', 'browser', 'playwright', 'credentials', 'evidence-dir']) {
  if (!values[name]) throw new Error('missing --' + name);
}
const app = path.resolve(values.app);
if (values['expected-binary-sha256'] && !/^[a-f0-9]{64}$/i.test(values['expected-binary-sha256'])) {
  throw new Error('--expected-binary-sha256 must be a SHA256 digest');
}
const evidence = path.resolve(values['evidence-dir']);
if (evidence === app || evidence.startsWith(app + path.sep)) {
  throw new Error('evidence must be outside the product workspace');
}
await mkdir(evidence, {recursive: true, mode: 0o700});
// Exclusive creation protects earlier evidence from an accidental repeat.
const reportPath = path.join(evidence, 'report.json');
await writeFile(reportPath, '', {flag: 'wx', mode: 0o600});
const fixture = JSON.parse(await readFile(values.credentials, 'utf8'));
const report = {
  qualification: 'runner-role-tasks-campaign-studio',
  started_at: new Date().toISOString(),
  app,
  checks: [],
  screenshots: [],
  limitations: ['SQLite only', 'No SMTP, billing, deployment, load or broad adversarial security qualification'],
  cleanup: {}
};
report.evidence_archive = 'evidence.tar.gz';
const secrets = Object.values(fixture.users).map(user => user.password).concat(fixture.totp_secret_key);
const redact = value => secrets.reduce((text, secret) => text.replaceAll(secret, '[redacted]'), String(value));
const scratch = await mkdtemp(path.join(os.tmpdir(), 'runner-campaign-check-'));
let service;
let serviceExit;
let serviceError;
let browser;
let activePage;
const browserErrors = [];

async function hash(file) {
  const digest = createHash('sha256');
  for await (const chunk of createReadStream(file)) digest.update(chunk);
  return digest.digest('hex');
}
async function command(binary, args, env, input) {
  const child = spawn(binary, args, {cwd: app, env, stdio: ['pipe', 'pipe', 'pipe']});
  let output = '';
  let oversized = false;
  const collect = chunk => {
    output += chunk.toString();
    if (Buffer.byteLength(output) > 64 << 10) {
      oversized = true;
      child.kill('SIGKILL');
    }
  };
  child.stdout.on('data', collect);
  child.stderr.on('data', collect);
  child.stdin.on('error', () => {});
  child.stdin.end(input);
  const timeout = setTimeout(() => child.kill('SIGKILL'), 60_000);
  try {
    const [code] = await once(child, 'exit');
    assert.ok(!oversized, 'fixture command output exceeded 64KiB');
    assert.equal(code, 0, path.basename(binary) + ' ' + args.slice(0, 2).join(' ') + ' failed: ' + redact(output).slice(-3000));
    return output.trim();
  } finally {
    clearTimeout(timeout);
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
  }
}
async function unusedPort() {
  const listener = createServer();
  listener.listen(0, '127.0.0.1');
  await once(listener, 'listening');
  const port = listener.address().port;
  listener.close();
  await once(listener, 'close');
  return port;
}
async function ready(url) {
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    if (serviceError) throw serviceError;
    if (service.exitCode !== null || service.signalCode !== null) throw new Error('app exited before readiness');
    try {
      const response = await fetch(url + '/api/auth/me', {signal: AbortSignal.timeout(1000)});
      await response.arrayBuffer();
      if ([200, 401, 403].includes(response.status)) return;
    } catch {}
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  throw new Error('app did not become ready within 20s');
}
async function uiCheck(id, action) {
  try {
    await action();
    report.checks.push({id, status: 'passed'});
  } catch (error) {
    throw new Error(id + ': ' + error.message, {cause: error});
  }
}
async function screenshot(page, name) {
  await page.screenshot({path: path.join(evidence, name), fullPage: false});
  report.screenshots.push(name);
}
function watch(page) {
  activePage = page;
  page.setDefaultTimeout(10_000);
  page.on('pageerror', error => {
    if (browserErrors.length < 20) browserErrors.push(redact(error.message).slice(0, 500));
  });
}
async function login(page, baseURL, key) {
  watch(page);
  await page.goto(baseURL + '/campaigns');
  await page.getByLabel('Email', {exact: true}).fill(fixture.users[key].email);
  await page.getByLabel('Password', {exact: true}).fill(fixture.users[key].password);
  const responsePromise = page.waitForResponse(response => response.url().endsWith('/api/auth/login') && response.request().method() === 'POST');
  await page.getByRole('button', {name: /^(Login|Sign in)$/i}).click();
  assert.equal((await responsePromise).status(), 200, 'real UI login failed');
  await page.goto(baseURL + '/campaigns');
  await page.getByRole('heading', {name: 'Campaigns', exact: true}).waitFor();
}
async function noOverflow(page) {
  const dimensions = await page.evaluate(() => ({width: window.innerWidth, content: document.documentElement.scrollWidth}));
  assert.ok(dimensions.content <= dimensions.width + 1, 'horizontal overflow: ' + JSON.stringify(dimensions));
}
try {
  const binary = path.join(app, '.qualification', 'server');
  report.binary_sha256 = await hash(binary);
  if (values['expected-binary-sha256']) {
    report.expected_binary_sha256 = values['expected-binary-sha256'].toLowerCase();
    assert.equal(report.binary_sha256, report.expected_binary_sha256, 'accepted executable identity changed');
  }
  report.checker_sha256 = await hash(fileURLToPath(import.meta.url));
  report.api_checks_sha256 = await hash(fileURLToPath(new URL('./api-checks.mjs', import.meta.url)));
  report.navigation_checks_sha256 = await hash(fileURLToPath(new URL('./navigation-checks.mjs', import.meta.url)));
  const env = {
    ...process.env,
    PATH: path.dirname(values.go) + path.delimiter + process.env.PATH,
    GOTOOLCHAIN: 'local',
    GENNY_ENV: 'dev',
    DB_DRIVER: 'sqlite',
    DATABASE_URL: 'file:' + path.join(scratch, 'qualification.db') + '?_foreign_keys=on',
    GENNY_AUTH_TOTP_SECRET_KEY: fixture.totp_secret_key
  };
  report.go = await command(values.go, ['version'], env);
  report.genny = await command(values.genny, ['version'], env);
  for (const tenant of fixture.tenants) {
    await command(values.genny, ['tenant', 'create', '--id', tenant.id, '--name', tenant.name, '--project', app], env);
  }
  for (const user of Object.values(fixture.users)) {
    const args = ['user', 'create', '--email', user.email, '--tenant', user.tenant_id, '--password-stdin', '--project', app];
    if (user.admin) args.push('--tenant-admin');
    await command(values.genny, args, env, user.password + '\n');
  }
  report.checks.push({id: 'fixture.real-tenants-and-password-users', status: 'passed'});
  const port = await unusedPort();
  const baseURL = 'http://127.0.0.1:' + port;
  report.base_url = baseURL;
  service = spawn(binary, [], {
    cwd: app,
    env: {...env, ADDR: '127.0.0.1:' + port},
    stdio: ['ignore', 'pipe', 'pipe']
  });
  serviceExit = new Promise(resolve => {
    service.once('exit', resolve);
    service.once('error', error => { serviceError = error; resolve(); });
  });
  let serverLog = '';
  const collect = chunk => { serverLog = (serverLog + chunk.toString()).slice(-8000); };
  service.stdout.on('data', collect);
  service.stderr.on('data', collect);
  await ready(baseURL).catch(error => { throw new Error(error.message + ': ' + redact(serverLog)); });
  await checkAPI(baseURL, fixture, report.checks);

  const {chromium} = await import(pathToFileURL(values.playwright).href);
  const {expect} = await import(new URL('./test.mjs', pathToFileURL(values.playwright)).href);
  browser = await chromium.launch({headless: true, executablePath: values.browser});
  report.browser = browser.version();
  const member = await browser.newContext({viewport: {width: 1366, height: 900}});
  const admin = await browser.newContext({viewport: {width: 1366, height: 900}});
  const foreign = await browser.newContext({viewport: {width: 1366, height: 900}});
  const publicContext = await browser.newContext({viewport: {width: 1366, height: 900}});
  const memberPage = await member.newPage();
  const adminPage = await admin.newPage();
  const foreignPage = await foreign.newPage();
  const publicPage = await publicContext.newPage();
  const name = 'UI campaign ' + randomUUID().slice(0, 8);
  const editedName = name + ' edited';
  const description = 'A real campaign created and edited through the browser.';
  let campaignID;
  let editorURL;
  await uiCheck('ui.real-member-login-and-create', async () => {
    await login(memberPage, baseURL, 'memberA');
    const tenant = fixture.tenants.find(item => item.id === fixture.users.memberA.tenant_id);
    await memberPage.waitForFunction(expected => {
      const visible = document.body.innerText;
      return visible.includes(expected.name) || visible.includes(expected.id);
    }, tenant);
    await memberPage.getByRole('button', {name: 'New campaign', exact: true})
      .or(memberPage.getByRole('link', {name: 'New campaign', exact: true})).click();
    await memberPage.getByLabel('Name', {exact: true}).fill(name);
    await memberPage.getByLabel('Description', {exact: true}).fill('Browser draft');
    const created = memberPage.waitForResponse(response => response.url().endsWith('/api/campaigns') && response.request().method() === 'POST');
    await memberPage.getByRole('button', {name: 'Save draft', exact: true}).click();
    const response = await created;
    assert.equal(response.status(), 201);
    campaignID = (await response.json()).id;
    assert.equal(typeof campaignID, 'string');
    await memberPage.goto(baseURL + '/campaigns');
    editorURL = await openCampaignEditor(memberPage, name, expect);
  });
  await uiCheck('ui.member-edits-and-cannot-publish', async () => {
    await memberPage.getByLabel('Name', {exact: true}).fill(editedName);
    await memberPage.getByLabel('Description', {exact: true}).fill(description);
    const saved = memberPage.waitForResponse(response => response.url().endsWith('/api/campaigns/' + campaignID) && ['PATCH', 'PUT'].includes(response.request().method()));
    await memberPage.getByRole('button', {name: 'Save changes', exact: true}).click();
    assert.equal((await saved).status(), 200);
    await memberPage.goto(editorURL);
    await memberPage.waitForFunction(expected => {
      const values = [...document.querySelectorAll('input, textarea')].map(element => element.value);
      return values.includes(expected.name) && values.includes(expected.description);
    }, {name: editedName, description});
    assert.equal(await memberPage.getByLabel('Name', {exact: true}).inputValue(), editedName);
    assert.equal(await memberPage.getByLabel('Description', {exact: true}).inputValue(), description);
    const publish = memberPage.getByRole('button', {name: 'Publish', exact: true});
    assert.ok(await publish.count() === 0 || await publish.isDisabled(), 'member has an enabled Publish button');
    await noOverflow(memberPage);
    await screenshot(memberPage, 'member-editor.png');
  });
  await uiCheck('ui.draft-preview-reveals-no-content', async () => {
    watch(publicPage);
    await publicPage.goto(baseURL + '/preview/' + campaignID);
    await publicPage.waitForLoadState('networkidle');
    assert.ok(!(await publicPage.locator('body').innerText()).includes(editedName));
    assert.ok(!(await publicPage.locator('body').innerText()).includes(description));
  });
  await uiCheck('ui.admin-publishes', async () => {
    await login(adminPage, baseURL, 'adminA');
    await adminPage.getByRole('link', {name: editedName, exact: true}).click();
    const published = adminPage.waitForResponse(response => response.url().endsWith('/api/campaigns/' + campaignID + '/publish') && response.request().method() === 'POST');
    await adminPage.getByRole('button', {name: 'Publish', exact: true}).click();
    assert.equal((await published).status(), 200);
    await adminPage.getByText('Published', {exact: true}).first().waitFor();
  });
  await uiCheck('ui.public-preview-desktop-and-mobile', async () => {
    activePage = publicPage;
    await publicPage.goto(baseURL + '/preview/' + campaignID);
    await publicPage.getByRole('heading', {name: editedName, exact: true}).waitFor();
    await publicPage.getByText(description, {exact: true}).waitFor();
    await noOverflow(publicPage);
    await screenshot(publicPage, 'published-preview.png');
    await publicPage.setViewportSize({width: 390, height: 844});
    await noOverflow(publicPage);
    await screenshot(publicPage, 'preview-mobile.png');
    await memberPage.setViewportSize({width: 390, height: 844});
    await noOverflow(memberPage);
  });
  await uiCheck('ui.foreign-tenant-reveals-no-campaign', async () => {
    await login(foreignPage, baseURL, 'adminB');
    assert.equal(await foreignPage.getByRole('link', {name: editedName, exact: true}).count(), 0);
    await foreignPage.goto(editorURL);
    await foreignPage.waitForLoadState('networkidle');
    assert.ok(!(await foreignPage.locator('body').innerText()).includes(editedName));
    assert.ok(!(await foreignPage.locator('body').innerText()).includes(description));
  });
  await uiCheck('ui.no-unhandled-browser-errors', async () => {
    assert.deepEqual(browserErrors, []);
  });
  assert.equal(await hash(binary), report.binary_sha256, 'accepted executable changed during qualification');
  report.status = 'passed';
} catch (error) {
  report.status = 'failed';
  report.failure = redact(error.stack || error.message).slice(0, 8000);
  if (activePage && !activePage.isClosed()) {
    try { await screenshot(activePage, 'failure.png'); } catch {}
  }
} finally {
  if (browser) {
    try { await browser.close(); report.cleanup.browser_closed = true; }
    catch (error) { report.cleanup.browser_error = redact(error.message); }
  }
  if (service) {
    try {
      if (!serviceError && service.exitCode === null && service.signalCode === null) service.kill('SIGTERM');
      const killed = setTimeout(() => service.kill('SIGKILL'), 5000);
      try { await serviceExit; } finally { clearTimeout(killed); }
      report.cleanup.server_stopped = true;
    } catch (error) { report.cleanup.server_error = redact(error.message); }
  }
  try { await rm(scratch, {recursive: true}); report.cleanup.scratch_removed = true; }
  catch (error) { report.cleanup.scratch_error = redact(error.message); report.cleanup.remaining_scratch = scratch; }
  if (Object.keys(report.cleanup).some(key => key.endsWith('_error'))) report.status = 'failed';
  report.finished_at = new Date().toISOString();
  await writeFile(reportPath, JSON.stringify(report, null, 2) + '\n', {mode: 0o600});
  try {
    await command('/usr/bin/tar', [
      '-czf', path.join(evidence, report.evidence_archive), '-C', evidence,
      'report.json', ...report.screenshots
    ], process.env);
  } catch (error) {
    report.status = 'failed';
    report.archive_error = redact(error.message);
    await writeFile(reportPath, JSON.stringify(report, null, 2) + '\n', {mode: 0o600});
  }
  process.stdout.write(JSON.stringify(report, null, 2) + '\n');
  if (report.status !== 'passed') process.exitCode = 1;
}
