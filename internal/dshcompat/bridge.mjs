import { createRequire } from 'node:module'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'
import { createInterface } from 'node:readline'

const protocolWrite = process.stdout.write.bind(process.stdout)
const stderrWrite = process.stderr.write.bind(process.stderr)
const log = (...values) => stderrWrite(`${values.map(formatLog).join(' ')}\n`)
console.log = log
console.info = log
console.warn = log
console.error = log

let host
let tempRoot
let closing = false
const calls = new Map()

function formatLog(value) {
  if (value instanceof Error) return value.stack ?? value.message
  if (typeof value === 'string') return value
  try { return JSON.stringify(value) } catch { return String(value) }
}

function reply(id, result, error) {
  const message = error === undefined
    ? { id, result }
    : { id, error: normalizeError(error) }
  protocolWrite(`${JSON.stringify(message)}\n`)
}

function normalizeError(error) {
  if (error instanceof Error) {
	const detail = { message: error.message, name: error.name, stack: error.stack }
	if (error.cause !== undefined) detail.cause = normalizeError(error.cause)
	if (error instanceof AggregateError) detail.errors = error.errors.map(normalizeError)
	return detail
  }
  return { message: formatLog(error), name: 'Error' }
}

function importFrom(anchor, specifier) {
  const require = createRequire(pathToFileURL(anchor).href)
  const resolved = require.resolve(specifier)
  return import(pathToFileURL(resolved).href)
}

function resolvePackageJSON(anchor, packageName) {
  const require = createRequire(pathToFileURL(anchor).href)
  return require.resolve(`${packageName}/package.json`)
}

function bundlePatch(packageJSONPath) {
  const manifest = JSON.parse(readFileSync(packageJSONPath, 'utf8'))
  const patch = manifest?.dsh?.bundle?.patch
  if (typeof patch !== 'string' || patch.length === 0) {
    throw new Error(`${manifest?.name ?? packageJSONPath} declares no dsh.bundle.patch`)
  }
  return {
    name: String(manifest.name ?? ''),
    path: resolve(dirname(packageJSONPath), patch),
  }
}

function resolvedLayerPatches(appBoot, layer, packageJSONPath) {
  const require = createRequire(pathToFileURL(packageJSONPath).href)
  const patches = appBoot.loadOverlayPatches('wg2-dsh-bridge', layer.path)
  for (const patch of patches) {
    if (!Array.isArray(patch?.insert)) continue
    for (const entry of patch.insert) {
      const name = entry?.name
      if (typeof name !== 'string' || name.startsWith('cordis:')) continue
      entry.name = pathToFileURL(require.resolve(name)).href
    }
  }
  return patches
}

async function initialize(spec) {
  if (host !== undefined) return describeHost()
  const workspace = resolve(String(spec.workspace || process.cwd()))
  const runtimeAnchor = resolve(String(spec.runtimeAnchor || ''))
  const selectedPackageJSON = resolve(String(spec.bundlePackageJSON || ''))
  if (!runtimeAnchor) throw new Error('runtimeAnchor is required')
  if (!selectedPackageJSON) throw new Error('bundlePackageJSON is required')

  process.chdir(workspace)
  if (spec.dshHome) process.env.DSH_HOME = resolve(String(spec.dshHome))
  process.env.DSH_PERMISSION_MODE ??= 'workspace-write'
  process.env.DSH_TELEMETRY_DISABLED ??= '1'
  process.env.DSH_TOOLS_MODE = 'native'

  const appBoot = await importFrom(runtimeAnchor, '@deepseek-ai/dsh-app-boot')
  const basePackageJSON = resolvePackageJSON(runtimeAnchor, '@deepseek-ai/dsh-base')
  const base = bundlePatch(basePackageJSON)
  const selected = bundlePatch(selectedPackageJSON)
  const layers = [base]
  if (selected.name !== base.name) layers.push(selected)
	const patches = layers.flatMap((layer) => resolvedLayerPatches(
		appBoot,
		layer,
		layer.name === base.name ? basePackageJSON : selectedPackageJSON,
	))

  tempRoot = mkdtempSync(join(tmpdir(), 'wg2-dsh-bridge-'))
  const emptyConfig = join(tempRoot, 'cordis.yml')
  writeFileSync(emptyConfig, '[]\n', 'utf8')
  const ctx = await appBoot.boot(
    'wg2-dsh-bridge',
    emptyConfig,
    patches,
    undefined,
	pathToFileURL(basePackageJSON).href,
  )
  const sessionId = `wg2-${process.pid}-${Date.now()}`
  const handle = await ctx.agents.create({ sessionId, meta: { cwd: workspace } })
  host = { ctx, handle, layers, workspace, runtimeAnchor }
  return describeHost()
}

function describeHost() {
  return {
    protocol: 1,
    layers: host.layers,
    workspace: host.workspace,
    runtimeAnchor: host.runtimeAnchor,
    tools: host.ctx.tools.schemas(host.handle.agent),
  }
}

async function callTool(requestId, params) {
  if (host === undefined) throw new Error('DSH host is not initialized')
  const controller = new AbortController()
  calls.set(requestId, controller)
  try {
    return await host.ctx.tools.execute({
      signal: controller.signal,
      callId: String(params.callId || `wg2-${requestId}`),
      name: String(params.name || ''),
      arguments: params.arguments ?? {},
      agent: host.handle.agent,
    })
  } finally {
    calls.delete(requestId)
  }
}

async function shutdown() {
  if (closing) return { closed: true }
  closing = true
  for (const controller of calls.values()) controller.abort('DSH bridge is shutting down')
  calls.clear()
  try {
    if (host !== undefined) {
      await host.handle.dispose()
      await host.ctx.fiber.dispose()
      host = undefined
    }
  } finally {
    if (tempRoot !== undefined) {
      rmSync(tempRoot, { recursive: true, force: true })
      tempRoot = undefined
    }
  }
  return { closed: true }
}

async function dispatch(message) {
  switch (message.method) {
    case 'initialize': return await initialize(message.params ?? {})
    case 'tools/list': return describeHost()
    case 'tools/call': return await callTool(message.id, message.params ?? {})
    case 'cancel': {
      const target = Number(message.params?.id)
      calls.get(target)?.abort('cancelled by WorkGround2')
      return { cancelled: calls.has(target) }
    }
    case 'shutdown': return await shutdown()
    default: throw new Error(`unknown bridge method ${JSON.stringify(message.method)}`)
  }
}

const lines = createInterface({ input: process.stdin, crlfDelay: Infinity })
lines.on('line', (line) => {
  let message
  try {
    message = JSON.parse(line)
  } catch (error) {
    reply(null, undefined, new Error(`invalid JSON request: ${formatLog(error)}`))
    return
  }
  Promise.resolve(dispatch(message)).then(
    result => reply(message.id, result),
    error => reply(message.id, undefined, error),
  )
})
lines.on('close', () => {
  void shutdown().finally(() => process.exit(0))
})
process.on('SIGTERM', () => { void shutdown().finally(() => process.exit(0)) })
process.on('SIGINT', () => { void shutdown().finally(() => process.exit(130)) })
