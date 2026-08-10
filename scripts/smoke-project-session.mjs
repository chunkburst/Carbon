import { spawn, spawnSync } from 'node:child_process'
import { once } from 'node:events'
import {
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  realpathSync,
  rmSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import readline from 'node:readline'

const binary = resolve(process.argv[2] ?? '')
if (!process.argv[2] || !existsSync(binary)) {
  throw new Error('usage: node scripts/smoke-project-session.mjs <carbon-binary>')
}

const tempBase = realpathSync(tmpdir())
const smokeRoot = mkdtempSync(join(tempBase, 'carbon-project-session-smoke-'))
const homeRoot = join(smokeRoot, 'home')
const sourceA = join(smokeRoot, 'alpha')
const sourceB = join(smokeRoot, 'beta')
mkdirSync(homeRoot)
mkdirSync(sourceA)
mkdirSync(sourceB)

let server
let stderr = ''

function assert(condition, message) {
  if (!condition) throw new Error(message)
}

function structured(result) {
  if (result?.structuredContent) return result.structuredContent
  const text = result?.content?.find((entry) => entry.type === 'text')?.text
  if (!text) throw new Error('MCP tool result has no structured content')
  return JSON.parse(text)
}

function countMarkdownFiles(directory) {
  return readdirSync(directory, { withFileTypes: true }).filter(
    (entry) => entry.isFile() && entry.name.endsWith('.md'),
  ).length
}

try {
  const initialized = spawnSync(binary, ['home', 'init', '--home', homeRoot], {
    encoding: 'utf8',
    windowsHide: true,
  })
  assert(
    initialized.status === 0,
    `home init failed: ${initialized.stderr || initialized.stdout}`,
  )

  server = spawn(
    binary,
    [
      'serve',
      '--actor',
      'agent:deploy-smoke',
      '--client',
      'deploy-smoke',
      '--home',
      homeRoot,
      '--project-session',
      '--compat-layer',
      'v2',
    ],
    { stdio: ['pipe', 'pipe', 'pipe'], windowsHide: true },
  )
  server.stderr.setEncoding('utf8')
  server.stderr.on('data', (chunk) => {
    stderr += chunk
  })

  const pending = new Map()
  let nextID = 1
  const lines = readline.createInterface({ input: server.stdout })
  lines.on('line', (line) => {
    let message
    try {
      message = JSON.parse(line)
    } catch {
      return
    }
    const request = pending.get(String(message.id))
    if (!request) return
    clearTimeout(request.timer)
    pending.delete(String(message.id))
    if (message.error) {
      request.reject(
        new Error(`MCP ${message.error.code}: ${message.error.message}`),
      )
      return
    }
    request.resolve(message.result)
  })
  server.on('exit', () => {
    for (const request of pending.values()) {
      clearTimeout(request.timer)
      request.reject(new Error(`MCP server exited early: ${stderr.trim()}`))
    }
    pending.clear()
  })

  const request = (method, params) => {
    const id = nextID++
    return new Promise((resolveRequest, rejectRequest) => {
      const timer = setTimeout(() => {
        pending.delete(String(id))
        rejectRequest(new Error(`MCP request timed out: ${method}`))
      }, 10_000)
      pending.set(String(id), {
        resolve: resolveRequest,
        reject: rejectRequest,
        timer,
      })
      server.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', id, method, params })}\n`)
    })
  }
  const notify = (method, params = {}) => {
    server.stdin.write(`${JSON.stringify({ jsonrpc: '2.0', method, params })}\n`)
  }
  const callTool = (name, args = {}) =>
    request('tools/call', { name, arguments: args })

  const initialize = await request('initialize', {
    protocolVersion: '2025-06-18',
    capabilities: {},
    clientInfo: { name: 'deploy-smoke', version: '1' },
  })
  notify('notifications/initialized')

  const initialIdentity = structured(await callTool('identity'))
  const beforeSelection = await callTool('create', {
    title: 'must fail before selection',
  })
  assert(beforeSelection.isError, 'task creation did not fail before selection')

  const createdA = await callTool('create_project', {
    name: 'Session Alpha',
    slug: 'session-alpha',
    source_path: sourceA,
    allow_create: true,
    reason: 'deployed Project Session smoke test',
  })
  assert(!createdA.isError, 'creating project A failed')
  const projectA = structured(createdA).project.canonicalId
  assert(projectA, 'project A canonical ID is missing')
  const taskA = await callTool('create', {
    title: 'Alpha active task',
    body: 'created through the deployed Project Session',
    type: 'foundation',
    importance: 'normal',
  })
  assert(!taskA.isError, `creating task A failed: ${JSON.stringify(taskA)}`)

  const createdB = await callTool('create_project', {
    name: 'Session Beta',
    slug: 'session-beta',
    source_path: sourceB,
    allow_create: true,
    reason: 'verify automatic active-project replacement',
  })
  assert(!createdB.isError, 'creating project B failed')
  const projectB = structured(createdB).project.canonicalId
  assert(projectB && projectB !== projectA, 'project B canonical ID is invalid')
  const identityB = structured(await callTool('identity'))
  assert(
    identityB.scope.projectId === projectB,
    'create_project did not automatically select project B',
  )
  const taskB = await callTool('create', {
    title: 'Beta active task',
    body: 'created after the automatic project switch',
    type: 'foundation',
    importance: 'normal',
  })
  assert(!taskB.isError, `creating task B failed: ${JSON.stringify(taskB)}`)

  const selectedA = await callTool('select_project', { project: projectA })
  assert(!selectedA.isError, 'select_project failed')
  const finalIdentity = structured(await callTool('identity'))
  assert(finalIdentity.scope.projectId === projectA, 'project A was not restored')
  const listedA = structured(await callTool('list'))
  assert(listedA.tasks.length === 1, 'project A task list leaked or lost tasks')

  const tasksA = countMarkdownFiles(
    join(homeRoot, '.carbon', 'projects', projectA, '.carbon', 'tasks'),
  )
  const tasksB = countMarkdownFiles(
    join(homeRoot, '.carbon', 'projects', projectB, '.carbon', 'tasks'),
  )
  assert(tasksA === 1 && tasksB === 1, 'physical project roots are not isolated')

  console.log(
    JSON.stringify({
      protocolVersion: initialize.protocolVersion,
      initialBindingMode: initialIdentity.bindingMode,
      preSelectionRejected: beforeSelection.isError,
      projectA,
      projectB,
      automaticSelection: identityB.scope.projectId,
      finalSelection: finalIdentity.scope.projectId,
      selectionVersion: finalIdentity.selectionVersion,
      tasksA,
      tasksB,
    }),
  )
} finally {
  if (server && server.exitCode === null) {
    server.stdin.end()
    await Promise.race([
      once(server, 'exit'),
      new Promise((resolveWait) => setTimeout(resolveWait, 2_000)),
    ])
    if (server.exitCode === null) server.kill()
  }

  const resolvedSmokeRoot = realpathSync(smokeRoot)
  assert(
    dirname(resolvedSmokeRoot).toLowerCase() === tempBase.toLowerCase() &&
      !lstatSync(resolvedSmokeRoot).isSymbolicLink(),
    `unsafe smoke cleanup target: ${resolvedSmokeRoot}`,
  )
  rmSync(resolvedSmokeRoot, { recursive: true, force: true })
}
