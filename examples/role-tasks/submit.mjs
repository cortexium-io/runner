#!/usr/bin/env node
import {readFile} from 'node:fs/promises';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {once} from 'node:events';
import {parseArgs} from 'node:util';

const {values} = parseArgs({options: Object.fromEntries([
  'runner', 'data', 'endpoint', 'project', 'process', 'request', 'request-id'
].map(name => [name, {type: 'string'}]))});
for (const name of ['runner', 'data', 'endpoint', 'project', 'request', 'request-id']) {
  if (!values[name]) throw new Error('missing --' + name);
}
for (const name of ['runner', 'data', 'request']) {
  if (!path.isAbsolute(values[name])) throw new Error('--' + name + ' must be absolute');
}
const endpoint = new URL(values.endpoint);
if (endpoint.protocol !== 'http:' || !['127.0.0.1', '[::1]', 'localhost'].includes(endpoint.hostname)) {
  throw new Error('qualification endpoint must be local HTTP');
}
const text = await readFile(values.request, 'utf8');
if (Buffer.byteLength(text) > 256 << 10) throw new Error('request file exceeds 256KiB');
const inputs = JSON.parse(text);
if (!inputs || typeof inputs.request !== 'string' || Object.keys(inputs).length !== 1) {
  throw new Error('request file must contain one request string');
}
const args = [
  'run', '--data', values.data, '--endpoint', values.endpoint,
  '--project', values.project, '--request-id', values['request-id'],
  '--inputs', JSON.stringify(inputs)
];
if (values.process) args.push('--process', values.process);
const child = spawn(values.runner, args, {stdio: 'inherit'});
const forward = signal => child.kill(signal);
const interrupt = () => forward('SIGINT');
const terminate = () => forward('SIGTERM');
process.on('SIGINT', interrupt);
process.on('SIGTERM', terminate);
try {
  const [code, signal] = await once(child, 'exit');
  process.exitCode = code === null ? (signal === 'SIGINT' ? 130 : 143) : code;
} finally {
  process.off('SIGINT', interrupt);
  process.off('SIGTERM', terminate);
}
