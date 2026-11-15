import { reactive } from 'vue'

export const metrics = reactive({ total: 0, server: 0 })
export const session = reactive({ user: '', role: 'anonymous', level: 0, version: '', anonymous_dashboard: true, setup: false, host_network: false, loaded: false })
export const events = { unauthorized: () => {} }

async function request(method, path, body, quiet) {
  const started = performance.now()
  const options = { method, headers: { 'X-Requested-With': 'proximity' }, credentials: 'same-origin' }
  if (body !== undefined) {
    options.headers['Content-Type'] = 'application/json'
    options.body = JSON.stringify(body)
  }
  const response = await fetch(path, options)
  let data = null
  try {
    data = await response.json()
  } catch (error) {
    data = null
  }
  if (!quiet) {
    metrics.total = performance.now() - started
    metrics.server = Number(response.headers.get('X-Proximity-Time') || 0)
  }
  if (!response.ok) {
    if (response.status === 401 && path !== '/api/login') {
      events.unauthorized()
    }
    throw new Error((data && data.error) || `request failed (${response.status})`)
  }
  return data
}

export const api = {
  get: (path, quiet) => request('GET', path, undefined, quiet),
  post: (path, body) => request('POST', path, body === undefined ? {} : body),
  put: (path, body) => request('PUT', path, body),
  remove: (path) => request('DELETE', path)
}

export async function loadSession() {
  Object.assign(session, await api.get('/api/session', true), { loaded: true })
}

export const canEdit = () => session.level >= 2
export const isAdmin = () => session.level >= 3
export const id = (value) => encodeURIComponent(value)
