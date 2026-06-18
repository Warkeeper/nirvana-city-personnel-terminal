(function (root, factory) {
  if (typeof module === 'object' && module.exports) {
    module.exports = factory()
  } else {
    root.NCFMSCloudSync = factory()
  }
})(typeof window !== 'undefined' ? window : globalThis, function () {
  const DEFAULT_ADMIN_BASE_URL = 'http://124.221.236.228:23456'
  const TOKEN_KEY = 'ncfms-cloud-token'
  const ADMIN_BASE_URL_KEY = 'ncfms-cloud-admin-base-url'

  function text(value) {
    return String(value == null ? '' : value).trim()
  }

  function number(value, fallback = 0) {
    const parsed = Number(value)
    return Number.isFinite(parsed) ? parsed : fallback
  }

  function array(value) {
    return Array.isArray(value) ? value : []
  }

  function codeSet(values) {
    return new Set(array(values).map(text).filter(Boolean))
  }

  function isTravelSuppressed(role, suppressedCodes) {
    const code = text(role && role.code)
    return Boolean(code && suppressedCodes.has(code))
  }

  function normalizeTime(value) {
    const raw = text(value)
    if (!raw) return ''
    const parsed = new Date(raw)
    if (!Number.isNaN(parsed.getTime())) return parsed.toISOString()

    const legacy = raw.match(/^(\d{4})-(\d{2})-(\d{2})_(\d{2})-(\d{2})-(\d{2})$/)
    if (legacy) {
      const [, y, m, day, h, min, sec] = legacy
      const date = new Date(Number(y), Number(m) - 1, Number(day), Number(h), Number(min), Number(sec))
      if (!Number.isNaN(date.getTime())) return date.toISOString()
    }
    return raw
  }

  function toSheetTime(value) {
    const raw = normalizeTime(value)
    if (!raw) return ''
    const date = new Date(raw)
    if (Number.isNaN(date.getTime())) return ''
    const pad = n => String(n).padStart(2, '0')
    return `${date.getFullYear()}/${date.getMonth() + 1}/${date.getDate()} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
  }

  function normalizeHistoryEntry(entry) {
    const raw = text(entry)
    if (!raw) return ''
    const [timePart, ...restParts] = raw.split(' 为 ')
    const normalizedTime = toSheetTime(timePart)
    if (!normalizedTime) return raw
    if (!restParts.length) return normalizedTime
    return `${normalizedTime} 为 ${restParts.join(' 为 ')}`
  }

  function splitRoles(source) {
    const roles = array(source && source.roles)
    return {
      players: roles.filter(role => role && role.type === 'player'),
      npcs: roles.filter(role => role && role.type === 'npc')
    }
  }

  function roleIdentityKey(role) {
    return `${text(role && role.code)}\u0000${text(role && role.name)}`
  }

  function npcDatabaseRow(role) {
    return {
      '姓名': text(role.name),
      '编号': text(role.code),
      '金条余额': number(role.balance),
      '常驻居民/城邦居民': '常驻居民',
      '当前身份': text(role.identityCurrent),
      '历史身份记录': '',
      '备注': text(role.remark)
    }
  }

  function buildExportWorkbookData(source) {
    const { players, npcs } = splitRoles(source || {})
    const historicalNpcs = array(source && source.historicalNpcs)
    const historicalPlayers = array(source && source.historicalPlayers)
    const suppressedTravelCodes = codeSet(source && source.suppressedTravelResidentCodes)
    const playerMap = new Map()
    const currentNpcByKey = new Map()
    const historicalNpcKeys = new Set()
    const emittedCurrentNpcKeys = new Set()

    npcs.forEach(role => {
      const code = text(role && role.code)
      if (!code) return
      currentNpcByKey.set(roleIdentityKey(role), role)
    })

    historicalPlayers.forEach(role => {
      const code = text(role && role.code)
      if (!code) return
      playerMap.set(code, {
        name: text(role.name),
        code,
        balance: number(role.balance),
        identityCurrent: text(role.identityCurrent),
        identityHistory: array(role.identityHistory).map(normalizeHistoryEntry).filter(Boolean),
        remark: text(role.remark)
      })
    })

    players.forEach(role => {
      const code = text(role && role.code)
      if (!code) return
      const existing = playerMap.get(code)
      playerMap.set(code, {
        name: text(role.name),
        code,
        balance: number(role.balance),
        identityCurrent: text(role.identityCurrent),
        identityHistory: array(role.identityHistory).map(normalizeHistoryEntry).filter(Boolean),
        remark: role.remark == null && existing ? existing.remark : text(role.remark)
      })
    })

    const npcRows = historicalNpcs.map(role => {
      const key = roleIdentityKey(role)
      historicalNpcKeys.add(key)
      const current = currentNpcByKey.get(key)
      if (current) emittedCurrentNpcKeys.add(key)
      const merged = current && current.remark == null ? { ...current, remark: role.remark } : current
      return npcDatabaseRow(merged || role)
    })
    npcs.forEach(role => {
      const key = roleIdentityKey(role)
      if (historicalNpcKeys.has(key) || emittedCurrentNpcKeys.has(key)) return
      npcRows.push(npcDatabaseRow(role))
    })

    const databaseRows = [
      ...npcRows,
      ...Array.from(playerMap.values()).map(role => ({
        '姓名': role.name,
        '编号': role.code,
        '金条余额': role.balance,
        '常驻居民/城邦居民': '城邦居民',
        '当前身份': role.identityCurrent,
        '历史身份记录': array(role.identityHistory).join(','),
        '备注': role.remark
      }))
    ]

    const recordRows = array(source && source.records).map(record => ({
      '时间': record.time,
      '编号': record.code,
      '名称': record.name,
      '当前身份': record.identity || '',
      '类型': record.type,
      '数量': record.amount,
      '操作后余额': record.balance,
      '备注': record.remark || '',
      '状态': record.voided ? '作废' : '有效'
    }))

    const travelRows = players
      .filter(role => !isTravelSuppressed(role, suppressedTravelCodes))
      .map(role => ({
        '姓名': text(role.name),
        '编号': text(role.code),
        '进城时间': toSheetTime(role.enterTime),
        '离城时间': toSheetTime(role.leaveTime),
        '时长增加记录': array(role.timeIncreaseLogs).map(log => `${toSheetTime(log.time)} +${number(log.minutes)}分钟`).join(',')
      }))

    return { databaseRows, recordRows, travelRows }
  }

  function roleToCloudPlayer(role) {
    return {
      code: text(role.code),
      name: text(role.name),
      identity: text(role.identityCurrent),
      gold: number(role.balance),
      residentType: role.type === 'npc' ? 'npc' : 'player'
    }
  }

  function recordToGoldRecord(record) {
    return {
      clientRecordId: text(record.id),
      playerCode: text(record.code),
      playerName: text(record.name),
      identity: text(record.identity),
      occurredAt: normalizeTime(record.time),
      operationType: text(record.type),
      amount: number(record.amount),
      balanceAfter: number(record.balance),
      remark: text(record.remark),
      voided: Boolean(record.voided),
      affectBalance: record.affectBalance !== false,
      operator: text(record.operator)
    }
  }

  function travelRecord(role, direction, occurredAt) {
    return {
      clientRecordId: `${text(role.id || role.code)}:${direction}`,
      playerCode: text(role.code),
      playerName: text(role.name),
      identity: text(role.identityCurrent),
      direction,
      occurredAt: normalizeTime(occurredAt),
      residentType: role.type === 'npc' ? 'npc' : 'player',
      remark: '',
      operator: ''
    }
  }

  function buildCloudSyncPayload(source) {
    const { players, npcs } = splitRoles(source || {})
    const suppressedTravelCodes = codeSet(source && source.suppressedTravelResidentCodes)
    const cloudPlayers = players.concat(npcs).map(roleToCloudPlayer).filter(player => player.code)
    const travelRecords = []
    players.forEach(role => {
      if (isTravelSuppressed(role, suppressedTravelCodes)) return
      if (text(role.enterTime)) travelRecords.push(travelRecord(role, 'enter', role.enterTime))
      if (text(role.leaveTime)) travelRecords.push(travelRecord(role, 'exit', role.leaveTime))
    })

    return {
      clientUploadedAt: new Date().toISOString(),
      players: cloudPlayers,
      goldRecords: array(source && source.records).map(recordToGoldRecord),
      travelRecords
    }
  }

  function normalizeBaseUrl(baseUrl) {
    return text(baseUrl || DEFAULT_ADMIN_BASE_URL).replace(/\/+$/, '')
  }

  function storedAdminBaseUrl(storage) {
    try {
      return normalizeBaseUrl((storage || localStorage).getItem(ADMIN_BASE_URL_KEY) || DEFAULT_ADMIN_BASE_URL)
    } catch (_) {
      return DEFAULT_ADMIN_BASE_URL
    }
  }

  function storeAdminBaseUrl(baseUrl, storage) {
    const normalized = normalizeBaseUrl(baseUrl)
    try {
      ;(storage || localStorage).setItem(ADMIN_BASE_URL_KEY, normalized)
    } catch (_) {}
    return normalized
  }

  function tokenStorage(storage) {
    return storage || sessionStorage
  }

  function getToken(storage) {
    try {
      return text(tokenStorage(storage).getItem(TOKEN_KEY))
    } catch (_) {
      return ''
    }
  }

  function setToken(token, storage) {
    try {
      tokenStorage(storage).setItem(TOKEN_KEY, text(token))
    } catch (_) {}
  }

  function clearToken(storage) {
    try {
      tokenStorage(storage).removeItem(TOKEN_KEY)
    } catch (_) {}
  }

  async function parseResponse(response) {
    const body = await response.json().catch(() => ({ code: response.status, message: response.statusText, data: null }))
    if (!response.ok || body.code !== 0) {
      const error = new Error(body.message || '云端请求失败')
      error.status = response.status
      error.code = body.code || response.status
      throw error
    }
    return body.data
  }

  async function login(options) {
    const fetchImpl = options.fetchImpl || fetch
    const baseUrl = normalizeBaseUrl(options.baseUrl)
    const password = text(options.password)
    const response = await fetchImpl(`${baseUrl}/api/ncfms/auth/login`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password })
    })
    const data = await parseResponse(response)
    setToken(data && data.token, options.sessionStorage)
    return data
  }

  async function upload(options) {
    const fetchImpl = options.fetchImpl || fetch
    const baseUrl = normalizeBaseUrl(options.baseUrl)
    const token = text(options.token || getToken(options.sessionStorage))
    const response = await fetchImpl(`${baseUrl}/api/ncfms/sync`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${token}`
      },
      body: JSON.stringify(options.payload || {})
    })
    return parseResponse(response)
  }

  return {
    DEFAULT_ADMIN_BASE_URL,
    TOKEN_KEY,
    ADMIN_BASE_URL_KEY,
    buildExportWorkbookData,
    buildCloudSyncPayload,
    clearToken,
    getToken,
    login,
    normalizeTime,
    storedAdminBaseUrl,
    storeAdminBaseUrl,
    upload
  }
})
