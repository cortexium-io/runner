// Runner-owned evaluator. It receives inert, sanitized metadata only. The
// container has no network; these two typed messages are its entire transport.
'use strict'
const fs = require('node:fs/promises')
const sync = require('node:fs')
const { createRequire } = require('node:module')
const roots = ['/usr/local/lib/node_modules/npm/', '/usr/lib/node_modules/npm/']
const root = roots.find(p => sync.existsSync(p + 'package.json'))
if (!root) throw new Error('pinned npm evaluator is missing')
const req = createRequire(root + 'package.json')
if (req('./package.json').version !== '11.19.0') throw new Error('unsupported npm evaluator version')
let sequence = 0
const pending = new Map()
async function transport(kind, data) {
  const id = ++sequence
  process.stdout.write(JSON.stringify({ id, kind, data }) + '\n')
  const path = '/output/reply-' + id + '.json'
  for (;;) {
    try {
      const body = await fs.readFile(path, 'utf8')
      return JSON.parse(body)
    } catch (e) {
      if (e.code !== 'ENOENT') throw e
      await new Promise(resolve => setTimeout(resolve, 5))
    }
  }
}
const fetchPath = req.resolve('npm-registry-fetch')
req(fetchPath)
require.cache[fetchPath].exports = async (path, options) => {
  if (path !== '/-/npm/v1/security/advisories/bulk' || options.method !== 'POST') throw new Error('unsupported audit request')
  const data = await transport('bulk', options.body)
  for (const advisories of Object.values(data)) {
    for (const advisory of advisories) {
      if (!req('semver').validRange(advisory.vulnerable_versions)) throw new Error('malformed advisory range')
    }
  }
  return { json: async () => data }
}
const pacote = req('pacote')
pacote.packument = name => {
  if (!pending.has(name)) pending.set(name, transport('package', name))
  return pending.get(name)
}

async function main() {
  const policy = JSON.parse(await fs.readFile('/input/policy.json', 'utf8'))
  const Arborist = req('@npmcli/arborist')
  const AuditReport = req('@npmcli/arborist/lib/audit-report.js')
  const arb = new Arborist({ path: '/input', cache: '/tmp/npm-cache', registry: 'https://registry.npmjs.org/', workspacesEnabled: false, ignoreScripts: true, packageLock: true })
  const tree = await arb.loadVirtual()
  if (tree.errors?.length) throw new Error('inconsistent dependency graph')
  const semver = req('semver')
  // Derive omission flags from the graph instead of trusting lockfile flags.
  for (const node of tree.inventory.values()) {
    node.dev = node.optional = node.devOptional = node.peer = node.extraneous = true
  }
  req('@npmcli/arborist/lib/calc-dep-flags.js')(tree)
  const omit = new Set(policy.omit || [])
  for (const node of tree.inventory.values()) {
    if (!semver.valid(node.version)) throw new Error('unsupported package version')
    const omitted = (omit.has('dev') && node.dev) || (omit.has('optional') && node.optional) || (omit.has('dev') && omit.has('optional') && node.devOptional) || (omit.has('peer') && node.peer)
    for (const edge of node.edgesOut.values()) {
      if (!semver.validRange(edge.spec)) throw new Error('unsupported dependency specification')
      // An omitted subtree may lack platform-only children. Included missing
      // requirements and invalid installed edges must still block evaluation.
      if (!edge.valid && !edge.optional && !(omitted && !edge.to)) throw new Error('inconsistent dependency edge')
      if (edge.error && edge.to) throw new Error('invalid locked dependency')
    }
  }
  const report = await AuditReport.load(tree, { omit: policy.omit || [], cache: '/tmp/npm-cache', registry: 'https://registry.npmjs.org/', audit: true })
  if (report.error) throw new Error('audit endpoint unavailable')
  await fs.writeFile('/output/report.json', JSON.stringify(report.toJSON()), { flag: 'wx' })
}
main().catch(() => { process.stderr.write('npm audit evaluator could not complete reliably\n'); process.exitCode = 125 })
