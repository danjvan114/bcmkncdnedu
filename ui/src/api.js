const JSON_HEADERS = { 'Content-Type': 'application/json' }

async function request(path, opts = {}) {
  let res
  try {
    res = await fetch('/api' + path, opts)
  } catch (e) {
    throw new Error('连不上本地服务，请重启应用')
  }
  let data = null
  try {
    data = await res.json()
  } catch (e) {
    data = null
  }
  if (!res.ok || (data && data.ok === false)) {
    throw new Error((data && data.error) || `请求失败（HTTP ${res.status}）`)
  }
  return data || {}
}

function post(path, body) {
  return request(path, {
    method: 'POST',
    headers: JSON_HEADERS,
    body: JSON.stringify(body || {})
  })
}

export const api = {
  state: () => request('/state'),
  job: () => request('/job'),

  importAccounts: (path = '') => post('/accounts/import', { path }),
  verify: (names = []) => post('/accounts/verify', { names }),
  removeAccounts: (names) => post('/accounts/del', { names }),
  clearAccounts: () => post('/accounts/clear'),

  pickFile: () => post('/pick/file'),
  pickFolder: () => post('/pick/folder'),
  saveSettings: (m3u8Dir) => post('/settings', { m3u8Dir }),

  uploadStart: (path, mode, account) => post('/upload/start', { path, mode, account }),
  uploadCancel: () => post('/upload/cancel'),

  openPath: (path, reveal = false) => post('/open', { path, reveal }),

  winMin: () => post('/window/min'),
  winMax: () => post('/window/max'),
  winClose: () => post('/window/close'),
  winDrag: () => post('/window/drag'),
  winResize: (edge) => post('/window/resize', { edge }),
  winState: () => request('/window/state')
}

// 前端展示用的大小 / 速度格式化
export function fmtSize(n) {
  n = Number(n) || 0
  if (n < 1024) return n + ' B'
  const u = ['KB', 'MB', 'GB', 'TB']
  let i = -1
  do {
    n /= 1024
    i++
  } while (n >= 1024 && i < u.length - 1)
  return n.toFixed(n >= 100 ? 0 : 1) + ' ' + u[i]
}

export function fmtSpeed(n) {
  return fmtSize(n) + '/s'
}

export function fmtDur(sec) {
  sec = Math.max(0, Math.floor(Number(sec) || 0))
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = sec % 60
  const p = (x) => String(x).padStart(2, '0')
  return h > 0 ? `${h}:${p(m)}:${p(s)}` : `${p(m)}:${p(s)}`
}
