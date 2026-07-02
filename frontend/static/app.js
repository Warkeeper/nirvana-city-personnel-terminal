(function () {
    function createDefaultSession() {
        return {
            roles: [],
            records: [],
            latestOperation: null,
            sessionStartedAt: new Date().toISOString(),
            theme: 'light',
            hiddenResidentCodes: [],
            hiddenNpcKeys: [],
            suppressedTravelResidentCodes: [],
            historicalNpcs: [],
            visibleNpcCodes: [],
            currentSession: null
        }
    }

    const defaultCloudSyncAdminBaseUrl = 'https://nirvana-notes.cn/nirvana-city-admin'

    function detectBasePath() {
        const marker = '/static/app.js'
        const scripts = Array.prototype.slice.call(document.scripts || [])
        const current = document.currentScript || scripts.find(script => {
            const source = script && (script.src || script.getAttribute('src') || '')
            return source.includes(marker)
        })
        const source = current && (current.src || current.getAttribute('src'))
        if (!source) return ''
        try {
            const pathname = new URL(source, window.location.href).pathname
            const index = pathname.lastIndexOf(marker)
            if (index <= 0) return ''
            return pathname.slice(0, index).replace(/\/+$/, '')
        } catch (err) {
            return ''
        }
    }

    const basePath = detectBasePath()

    function appPath(path) {
        if (!path || path[0] !== '/') return path
        return basePath + path
    }

    function ensureOfflineDepsReady() {
        const missing = []
        if (!window.Vue) missing.push('Vue')
        if (!window.ELEMENT) missing.push('Element UI')
        if (!missing.length) return true
        const message = `本地依赖加载失败：${missing.join('、')}`
        const app = document.getElementById('app')
        if (app) {
            app.innerHTML = '<div style="padding:16px;border:1px solid #f56c6c;background:#fef0f0;color:#f56c6c;border-radius:6px;line-height:1.7;white-space:pre-line;">' + message + '</div>'
        } else {
            alert(message)
        }
        return false
    }

    if (!ensureOfflineDepsReady()) throw new Error('Offline dependencies missing')

    new Vue({
        el: '#app',
        data: {
            session: createDefaultSession(),
            theme: 'light',
            csrfToken: '',
            serverStats: {todayEntered: 0, currentInCity: 0, dailyExpense: 0},
            operateVisible: false,
            identityVisible: false,
            timeVisible: false,
            summaryVisible: false,
            addVisible: false,
            npcAddVisible: false,
            npcConfigVisible: false,
            maintenanceVisible: false,
            maintenanceQuery: '',
            maintenanceSearchPerformed: false,
            maintenanceResults: [],
            maintenanceSelectedRole: null,
            openCityVisible: false,
            openCityOperator: '',
            openCityDate: '',
            openCityTimeMode: '14:30',
            openCityCustomTime: '',
            openCityTimePresets: ['14:30', '18:30'],
            newRoleName: '',
            newRoleIdentity: '',
            newRoleDepartment: '城防部',
            newRoleCustomDepartment: '',
            newRoleBalance: 0,
            newRoleCode: '',
            newRoleEnterTime: '',
            newRoleStayHours: 2,
            stayHourMarks: {'0.5': '0.5', '1': '1', '1.5': '1.5', '2': '2', '2.5': '2.5', '3': '3', '3.5': '3.5', '4': '4', '4.5': '4.5', '5': '5', '5.5': '5.5', '6': '6', '6.5': '6.5', '7': '7'},
            newRoleIdentityHistory: [],
            operateType: 'allocate',
            operateAmount: 1,
            operateRemark: '',
            allocateCategory: '工资',
            allocateCustomReason: '',
            currentRole: null,
            summaryRole: null,
            summary: {},
            newRoleCodeDisable: false,
            newRoleNameDisable: false,
            roleSearchPerformed: false,
            roleMatchedCandidates: [],
            roleSelectedCandidateKey: '',
            playerSearchQuery: '',
            playerSearchVisible: true,
            playerSearchPerformed: false,
            playerSearchResults: [],
            playerProfileVisible: false,
            playerProfileRole: null,
            residentProfileVisible: false,
            residentProfileRole: null,
            residentProfileOriginalName: '',
            residentProfileOriginalCode: '',
            residentProfileName: '',
            residentProfileCode: '',
            residentProfileRemark: '',
            npcSearchCode: '',
            npcSearchName: '',
            npcSearchPerformed: false,
            npcMatchedCandidates: [],
            npcAddSearchCode: '',
            npcAddSearchName: '',
            npcAddSearchPerformed: false,
            npcAddMatchedCandidates: [],
            npcAddSelectedCode: '',
            npcNewName: '',
            npcNewCode: '',
            npcNewIdentity: '',
            npcNewBalance: 0,
            nowTimestamp: Date.now(),
            nowTicker: null,
            identityDepartment: '城防部',
            identityCustomDepartment: '',
            identityStage: '实习中',
            timeAddHours: 0.5,
            departments: ['城防部', '保安部', '探险家公会', '物资部', '丧尸管理部', '自由人', '其它'],
            enterTimeGroups: [
                {label: '下午场', times: ['14:30', '15:00', '15:30', '16:00', '16:30', '17:00']},
                {label: '晚上场', times: ['18:30', '19:00', '19:30', '20:00', '20:30', '21:00', '21:30', '22:00', '22:30', '23:00', '23:30']}
            ],
            identityStages: ['实习中', '实习完成', '入职考核', '正式员工'],
            matchedHistoricalPlayer: null,
            cloudSyncVisible: false,
            cloudSyncLoading: false,
            cloudSyncAdminBaseUrl: '',
            cloudSyncPassword: '',
            cloudSyncToken: '',
            cloudSyncResult: null,
            cloudSyncError: ''
        },
        computed: {
            roles() { return this.session.roles || [] },
            records() { return this.session.records || [] },
            players() { return this.roles.filter(r => r.type === 'player') },
            npcs() { return this.roles.filter(r => r.type === 'npc') },
            sortedPlayers() {
                const hiddenCodes = new Set((this.session.hiddenResidentCodes || []).map(code => this.normalizeResidentCode(code)).filter(Boolean))
                const getLeaveAt = (player) => {
                    const time = new Date(player.leaveTime).getTime()
                    return Number.isFinite(time) ? time : Number.MAX_SAFE_INTEGER
                }
                return this.players.filter(player => !hiddenCodes.has(this.normalizeResidentCode(player.code))).sort((a, b) => {
                    const leaveDiff = getLeaveAt(a) - getLeaveAt(b)
                    if (leaveDiff !== 0) return leaveDiff
                    return String(a.code || '').trim().localeCompare(String(b.code || '').trim(), 'zh-Hans-CN', {sensitivity: 'base'})
                })
            },
            cityResidentStats() {
                return {
                    todayEntered: this.serverStats.todayEntered || 0,
                    currentInCity: this.serverStats.currentInCity || 0
                }
            },
            groupedNpcs() {
                const groups = new Map()
                const hiddenNpcKeys = new Set(this.session.hiddenNpcKeys || [])
                this.npcs.filter(npc => !hiddenNpcKeys.has(this.npcVisibilityKey(npc))).forEach((npc) => {
                    const rawIdentity = String(npc.identityCurrent || '').trim()
                    const department = this.departments.find(dept => rawIdentity.startsWith(dept)) || '未分组'
                    if (!groups.has(department)) groups.set(department, [])
                    groups.get(department).push(npc)
                })
                return this.departments
                    .filter(dept => groups.has(dept))
                    .map(dept => ({department: dept, members: groups.get(dept)}))
                    .concat(Array.from(groups.keys()).filter(dept => !this.departments.includes(dept)).map(dept => ({department: dept, members: groups.get(dept)})))
            },
            dailyExpense() { return this.serverStats.dailyExpense || 0 },
            latestOperationTimeText() { return this.session.latestOperation?.time || '暂无' },
            latestOperationContentText() { return this.session.latestOperation?.content || '暂无' },
            needsImport() { return !this.session.currentSession },
            newResidentIdentityPreview() {
                return this.newRoleDepartment ? this.buildIdentity(this.newRoleDepartment, '实习中', this.newRoleCustomDepartment) : ''
            },
            selectedIdentityPreview() {
                return this.identityDepartment ? this.buildIdentity(this.identityDepartment, this.identityStage, this.identityCustomDepartment) : ''
            },
            cloudSyncResultRows() {
                const result = this.cloudSyncResult || {}
                const row = (label, key) => {
                    const stats = result[key] || {}
                    return {
                        label,
                        created: stats.created || 0,
                        updated: stats.updated || 0,
                        unchanged: stats.unchanged || 0
                    }
                }
                return [row('居民档案', 'residents'), row('金条流水', 'goldRecords'), row('进出城记录', 'travelRecords')]
            },
            cloudSyncArchivedText() {
                if (!this.cloudSyncResult) return ''
                return this.cloudSyncResult.archivedPayload ? '已归档' : '未归档'
            }
        },
        methods: {
            idempotencyKey() {
                return `${Date.now()}-${Math.random().toString(16).slice(2)}`
            },
            async api(path, options = {}) {
                const method = options.method || 'GET'
                const headers = Object.assign({'Accept': 'application/json'}, options.headers || {})
                let body = options.body
                if (body && typeof body !== 'string') {
                    headers['Content-Type'] = 'application/json'
                    body = JSON.stringify(body)
                }
                if (method !== 'GET' && method !== 'HEAD') {
                    headers['X-NCFMS-CSRF'] = this.csrfToken
                    headers['Idempotency-Key'] = options.idempotencyKey || this.idempotencyKey()
                }
                const response = await fetch(appPath(path), {method, headers, body})
                const contentType = response.headers.get('content-type') || ''
                const data = contentType.includes('application/json') ? await response.json() : await response.text()
                if (!response.ok) {
                    const message = data && data.error ? data.error : `请求失败：${response.status}`
                    throw new Error(message)
                }
                return data
            },
            applyState(data) {
                if (!data) return
                if (data.csrfToken) this.csrfToken = data.csrfToken
                if (data.session) {
                    this.session = Object.assign(createDefaultSession(), data.session)
                    this.theme = this.session.theme || this.theme || 'light'
                }
                this.serverStats = data.stats || this.serverStats
                this.refreshSelectedRoles()
            },
            refreshSelectedRoles() {
                if (this.currentRole) {
                    const next = this.findRoleByCode(this.currentRole.code)
                    if (next) this.currentRole = next
                }
                if (this.playerProfileRole) {
                    const next = this.findRoleByCode(this.playerProfileRole.code)
                    if (next) this.playerProfileRole = next
                }
                if (this.residentProfileRole) {
                    const next = this.findResidentByCode(this.residentProfileRole.code)
                    if (next) this.residentProfileRole = next
                }
                if (this.maintenanceSelectedRole) {
                    const next = this.findRoleByCode(this.maintenanceSelectedRole.code)
                    if (next) this.maintenanceSelectedRole = next
                }
            },
            async refresh() {
                const data = await this.api('/api/v1/bootstrap')
                this.applyState(data)
            },
            async write(path, body, options = {}) {
                const data = await this.api(path, Object.assign({}, options, {method: options.method || 'POST', body}))
                this.applyState(data)
                return data
            },
            async searchResidents(params) {
                const query = new URLSearchParams()
                Object.keys(params || {}).forEach((key) => {
                    const value = params[key]
                    if (value !== undefined && value !== null && String(value).trim() !== '') {
                        query.set(key, String(value).trim())
                    }
                })
                const data = await this.api(`/api/v1/residents/search?${query.toString()}`)
                return data.residents || []
            },
            async save() {},
            async load() { await this.refresh() },
            normalizeResidentCode(code) { return String(code || '').trim() },
            getNpcGroupColumns(group) {
                return Math.min(4, Math.max(1, (group?.members || []).length))
            },
            getNpcGroupStyle(group) {
                const columns = this.getNpcGroupColumns(group)
                const cardWidth = 176
                const gap = 8
                return {
                    '--npc-group-columns': String(columns),
                    '--npc-group-width': `${columns * cardWidth + (columns - 1) * gap}px`
                }
            },
            formatStayHoursTooltip(value) {
                const hours = Number(value)
                return Number.isFinite(hours) ? `${Number(hours.toFixed(1))} 小时` : ''
            },
            markOperation() {},
            resetPlayerCardSearch() {
                this.playerSearchPerformed = false
                this.playerSearchResults = []
            },
            searchPlayerCards() {
                const query = String(this.playerSearchQuery || '').trim()
                if (!query) {
                    this.resetPlayerCardSearch()
                    return this.$message.warning('请输入编号或姓名后再搜索')
                }
                const codeQuery = this.normalizeResidentCode(query)
                const nameQuery = query.toLowerCase()
                const matches = []
                for (const player of this.sortedPlayers) {
                    const codeMatched = this.normalizeResidentCode(player.code).includes(codeQuery)
                    const nameMatched = String(player.name || '').trim().toLowerCase().includes(nameQuery)
                    if (codeMatched || nameMatched) matches.push(player)
                    if (matches.length >= 5) break
                }
                this.playerSearchPerformed = true
                this.playerSearchResults = matches
                if (!matches.length) return this.$message.warning('未找到匹配的城邦居民')
                this.$message.success(`找到 ${matches.length} 位城邦居民`)
            },
            selectPlayerSearchResult(role) { this.openPlayerProfile(role) },
            openPlayerProfile(role) {
                if (!role) return
                this.playerProfileRole = role
                this.playerProfileVisible = true
            },
            closePlayerProfile() {
                this.playerProfileVisible = false
                this.playerProfileRole = null
            },
            npcVisibilityKey(role) {
                const code = this.normalizeResidentCode(role && role.code)
                const name = String((role && role.name) || '').trim()
                return code && name ? `${code}\u0000${name}` : ''
            },
            isPlayerHidden(role) {
                const code = this.normalizeResidentCode(role && role.code)
                return Boolean(code && (this.session.hiddenResidentCodes || []).some(hiddenCode => this.normalizeResidentCode(hiddenCode) === code))
            },
            isNpcHidden(role) {
                const key = this.npcVisibilityKey(role)
                return Boolean(key && (this.session.hiddenNpcKeys || []).includes(key))
            },
            isPlaceholderResidentName(name) { return String(name || '').trim() === '%暂未登记姓名%' },
            displayResidentName(name) {
                const normalizedName = String(name || '').trim()
                return this.isPlaceholderResidentName(normalizedName) ? '暂未登记姓名' : (normalizedName || '暂未登记姓名')
            },
            residentCodeExists(collection, code) {
                const normalizedCode = this.normalizeResidentCode(code)
                return Boolean(normalizedCode && (collection || []).some(role => this.normalizeResidentCode(role.code) === normalizedCode))
            },
            ensureSessionVisibilityCollections() {},
            ensureSessionNpcCollections() {},
            triggerImport() { this.$message.info('Excel 导入已移除：SQLite 是唯一业务事实来源。') },
            importHistoryData() { this.triggerImport() },
            toSheetTime(v) {
                if (!v) return ''
                const d = new Date(v)
                if (Number.isNaN(d.getTime())) return ''
                const pad = n => String(n).padStart(2, '0')
                return `${d.getFullYear()}/${d.getMonth() + 1}/${d.getDate()} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
            },
            formatHistoryTime(v) { return this.toSheetTime(v) },
            normalizeHistoryEntry(entry) {
                if (entry && typeof entry === 'object') return entry.display || ''
                return String(entry || '').trim()
            },
            syncPlayerToHistorical() {},
            syncNpcToHistorical() {},
            syncResidentRecordNames() { return 0 },
            resetMaintenanceSearch() {
                this.maintenanceQuery = ''
                this.maintenanceSearchPerformed = false
                this.maintenanceResults = []
            },
            resetMaintenanceDialog() {
                this.resetMaintenanceSearch()
                this.maintenanceSelectedRole = null
            },
            openResidentMaintenance() {
                this.resetMaintenanceSearch()
                this.maintenanceSelectedRole = null
                this.maintenanceVisible = true
            },
            residentKindLabel(role) {
                return role && role.type === 'npc' ? '常驻居民' : '城邦居民'
            },
            async searchMaintenanceResidents() {
                const query = String(this.maintenanceQuery || '').trim()
                if (!query) {
                    this.resetMaintenanceSearch()
                    return this.$message.warning('请输入编号或姓名后再搜索')
                }
                try {
                    const matches = await this.searchResidents({q: query, limit: 10})
                    this.maintenanceSearchPerformed = true
                    this.maintenanceResults = matches.map((role, index) => ({...role, _candidateKey: `${role.code}-${index}`}))
                    if (!matches.length) return this.$message.warning('未找到匹配的居民')
                    this.$message.success(`找到 ${matches.length} 位居民`)
                } catch (err) {
                    this.$message.error(err.message || err)
                }
            },
            selectMaintenanceResident(role) {
                if (!role) return
                this.ensureResidentFields(role)
                this.maintenanceSelectedRole = this.currentRole
            },
            openMaintenanceGold() {
                if (!this.maintenanceSelectedRole) return this.$message.warning('请先选择居民')
                this.openGoldManage(this.maintenanceSelectedRole)
            },
            openMaintenanceIdentity() {
                if (!this.maintenanceSelectedRole) return this.$message.warning('请先选择居民')
                this.openIdentityManage(this.maintenanceSelectedRole)
            },
            openMaintenanceProfile() {
                if (!this.maintenanceSelectedRole) return this.$message.warning('请先选择居民')
                this.editResidentProfile(this.maintenanceSelectedRole)
            },
            syncResidentReferences(role, oldCode = '') {
                if (!role) return
                const nextCode = this.normalizeResidentCode(role.code)
                const previousCode = this.normalizeResidentCode(oldCode)
                const matches = (target) => {
                    const code = this.normalizeResidentCode(target && target.code)
                    return Boolean(code && (code === nextCode || (previousCode && code === previousCode)))
                }
                if (matches(this.currentRole)) this.currentRole = role
                if (matches(this.playerProfileRole)) this.playerProfileRole = role
                if (matches(this.residentProfileRole)) this.residentProfileRole = role
                if (matches(this.maintenanceSelectedRole)) this.maintenanceSelectedRole = role
                this.maintenanceResults = this.maintenanceResults.map(item => matches(item) ? Object.assign({}, role, {_candidateKey: item._candidateKey}) : item)
            },
            async refreshMaintenanceSelectedByCode(code, oldCode = '') {
                const normalized = this.normalizeResidentCode(code)
                if (!normalized) return
                if (!this.maintenanceVisible && !this.maintenanceSelectedRole) return
                const matches = await this.searchResidents({code: normalized, limit: 5})
                const next = matches.find(role => this.normalizeResidentCode(role.code) === normalized)
                if (next) this.syncResidentReferences(next, oldCode)
            },
            resetResidentProfileForm() {
                this.residentProfileRole = null
                this.residentProfileOriginalName = ''
                this.residentProfileOriginalCode = ''
                this.residentProfileName = ''
                this.residentProfileCode = ''
                this.residentProfileRemark = ''
            },
            editResidentProfile(role) {
                if (!role || (role.type !== 'player' && role.type !== 'npc')) return
                this.residentProfileRole = role
                this.residentProfileOriginalName = String(role.name || '').trim()
                this.residentProfileOriginalCode = this.normalizeResidentCode(role.code)
                this.residentProfileName = this.residentProfileOriginalName
                this.residentProfileCode = this.residentProfileOriginalCode
                this.residentProfileRemark = String(role.remark || '').trim()
                this.residentProfileVisible = true
            },
            async submitResidentProfile() {
                const role = this.residentProfileRole
                if (!role) return
                const oldCode = this.normalizeResidentCode(role.code)
                const nextCode = this.normalizeResidentCode(this.residentProfileCode)
                const nextName = String(this.residentProfileName || '').trim()
                const nextRemark = String(this.residentProfileRemark || '').trim()
                if (!nextCode) return this.$message.warning('编号不能为空')
                if (!nextName) return this.$message.warning('姓名不能为空')
                if (this.isPlaceholderResidentName(nextName)) return this.$message.warning('请填写真实姓名')
                try {
                    await this.write(`/api/v1/residents/${encodeURIComponent(oldCode)}/profile`, {code: nextCode, name: nextName, remark: nextRemark}, {method: 'PATCH'})
                    const nextRole = this.findRoleByCode(nextCode)
                    if (nextRole) this.syncResidentReferences(nextRole, oldCode)
                    await this.refreshMaintenanceSelectedByCode(nextCode, oldCode)
                    this.residentProfileVisible = false
                    this.$message.success('居民资料已更新')
                } catch (err) {
                    this.$message.error(err.message)
                }
            },
            buildIdentity(department, stage, customDepartment = '') {
                if (!department) return ''
                if (department === '自由人') return '自由人'
                const identityDepartment = department === '其它' ? String(customDepartment || '').trim() : department
                if (!identityDepartment || !stage) return ''
                return `${identityDepartment}${stage}`
            },
            parseIdentityText(identityText) {
                const raw = String(identityText || '').trim()
                if (!raw || raw === '未设置') return {department: this.departments[0], stage: this.identityStages[0], customDepartment: ''}
                if (raw === '自由人') return {department: '自由人', stage: this.identityStages[0], customDepartment: ''}
                const matchedStage = this.identityStages.find(x => raw.endsWith(x))
                const stage = matchedStage || this.identityStages[0]
                const departmentText = matchedStage ? raw.slice(0, -matchedStage.length).trim() : raw
                const fixedDepartments = this.departments.filter(dept => dept !== '自由人' && dept !== '其它')
                const department = fixedDepartments.includes(departmentText) ? departmentText : '其它'
                const customDepartment = department === '其它' ? departmentText : ''
                return {department, stage, customDepartment}
            },
            async exportCurrentData() {
                const link = document.createElement('a')
                const now = new Date()
                const pad = n => String(n).padStart(2, '0')
                link.href = appPath('/api/v1/export/full.xlsx')
                link.download = `涅槃城账本-${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}-${pad(now.getHours())}${pad(now.getMinutes())}${pad(now.getSeconds())}.xlsx`
                document.body.appendChild(link)
                link.click()
                link.remove()
            },
            formatLocalDateValue(date = new Date()) {
                const pad = n => String(n).padStart(2, '0')
                return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`
            },
            getDefaultOpenCityTime() {
                const now = new Date()
                const minutes = now.getHours() * 60 + now.getMinutes()
                return minutes >= 18 * 60 + 30 ? '18:30' : '14:30'
            },
            getSelectedOpenCityTime() {
                return this.openCityTimeMode === 'custom' ? String(this.openCityCustomTime || '').trim() : this.openCityTimeMode
            },
            buildOpenCityOpenedAt() {
                const date = String(this.openCityDate || '').trim()
                const time = this.getSelectedOpenCityTime()
                return `${date} ${time}:00`
            },
            openCity() {
                this.openCityOperator = ''
                this.openCityDate = this.formatLocalDateValue(new Date())
                this.openCityTimeMode = this.getDefaultOpenCityTime()
                this.openCityCustomTime = ''
                this.openCityVisible = true
            },
            async submitOpenCity() {
                const operator = String(this.openCityOperator || '').trim()
                if (!operator) return this.$message.warning('操作员不能为空')
                if (!this.openCityDate) return this.$message.warning('请选择开城日期')
                const time = this.getSelectedOpenCityTime()
                if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(time)) return this.$message.warning('请输入有效开城时间')
                try {
                    await this.write('/api/v1/city/open', {operator, openedAt: this.buildOpenCityOpenedAt()})
                    this.openCityVisible = false
                    this.$message.success('开城完成')
                } catch (err) {
                    this.$message.error(err.message || err)
                }
            },
            async closeCity() {
                if (this.needsImport) return this.$message.warning('当前尚未开城，无需闭城。')
                try {
                    await this.$confirm('确认闭城吗？闭城会记录关城时间，并清空当前“城邦居民”面板；进出城记录仍会保留在导出账本中。', '闭城', {
                        confirmButtonText: '闭城',
                        cancelButtonText: '取消',
                        type: 'warning',
                        distinguishCancelAndClose: true
                    })
                    await this.write('/api/v1/city/close', {})
                    this.$message.success('闭城完成')
                } catch (err) {
                    if (err !== 'cancel' && err !== 'close') this.$message.error(err.message || err)
                }
            },
            openAdd() {
                if (this.needsImport) return this.$message.warning('请先开城，再执行该操作。')
                this.newRoleName = ''
                this.newRoleIdentity = ''
                this.newRoleDepartment = this.departments[0]
                this.newRoleCustomDepartment = ''
                this.newRoleCode = ''
                this.newRoleBalance = 0
                this.newRoleEnterTime = this.getDefaultEnterTime()
                this.newRoleStayHours = 2
                this.newRoleIdentityHistory = []
                this.newRoleCodeDisable = false
                this.newRoleNameDisable = false
                this.roleSearchPerformed = false
                this.roleMatchedCandidates = []
                this.roleSelectedCandidateKey = ''
                this.matchedHistoricalPlayer = null
                this.addVisible = true
            },
            cancelAddRole() {
                this.addVisible = false
                this.newRoleCodeDisable = false
                this.newRoleNameDisable = false
                this.roleSearchPerformed = false
                this.roleMatchedCandidates = []
                this.roleSelectedCandidateKey = ''
                this.newRoleIdentity = ''
                this.newRoleCustomDepartment = ''
                this.newRoleIdentityHistory = []
                this.matchedHistoricalPlayer = null
            },
            resetNpcAddForm() {
                this.npcAddSearchCode = ''
                this.npcAddSearchName = ''
                this.npcAddSearchPerformed = false
                this.npcAddMatchedCandidates = []
                this.npcAddSelectedCode = ''
                this.npcNewName = ''
                this.npcNewCode = ''
                this.npcNewIdentity = ''
                this.npcNewBalance = 0
            },
            resetNpcConfigSearch() {
                this.npcSearchCode = ''
                this.npcSearchName = ''
                this.npcSearchPerformed = false
                this.npcMatchedCandidates = []
            },
            openNpcConfig() {
                this.resetNpcConfigSearch()
                this.npcConfigVisible = true
            },
            openAddNpc() {
                if (this.needsImport) return this.$message.warning('请先开城，再执行该操作。')
                this.resetNpcAddForm()
                this.npcAddVisible = true
            },
            cancelNpcConfig() {
                this.npcConfigVisible = false
                this.resetNpcConfigSearch()
            },
            cancelAddNpc() {
                this.npcAddVisible = false
                this.resetNpcAddForm()
            },
            async searchAddNpc() {
                const codeQuery = this.normalizeResidentCode(this.npcAddSearchCode)
                const nameQuery = String(this.npcAddSearchName || '').trim()
                if (!codeQuery && !nameQuery) return this.$message.warning('请输入编号或姓名后再搜索')
                try {
                    const matches = await this.searchResidents({kind: 'npc', code: codeQuery, name: nameQuery, limit: 5})
                    this.npcAddSearchPerformed = true
                    this.npcAddMatchedCandidates = matches.map((npc, index) => ({...npc, _candidateKey: `${npc.code}-${index}`}))
                    this.npcAddSelectedCode = ''
                    if (!matches.length) {
                        this.npcNewCode = String(this.npcAddSearchCode || '').trim()
                        this.npcNewName = String(this.npcAddSearchName || '').trim()
                        this.npcNewIdentity = ''
                        this.npcNewBalance = 0
                        return this.$message.warning('未找到匹配的常驻居民，可手动新增')
                    }
                    this.selectAddNpcCandidate(this.npcAddMatchedCandidates[0]._candidateKey)
                    this.$message.success(`找到 ${matches.length} 位常驻居民`)
                } catch (err) {
                    this.$message.error(err.message)
                }
            },
            selectAddNpcCandidate(candidateKey) {
                const candidate = this.npcAddMatchedCandidates.find(npc => npc._candidateKey === candidateKey)
                if (!candidate) return
                this.npcAddSelectedCode = candidate._candidateKey
                this.npcNewName = candidate.name || ''
                this.npcNewCode = candidate.code || ''
                this.npcNewIdentity = candidate.identityCurrent || ''
                this.npcNewBalance = Number(candidate.balance || 0)
            },
            async addNpcRole() {
                const name = String(this.npcNewName || '').trim()
                const code = String(this.npcNewCode || '').trim()
                const identity = String(this.npcNewIdentity || '').trim() || '未设置'
                const balance = Number(this.npcNewBalance || 0)
                if (!name || !code) return this.$message.error('请填写姓名和编号')
                if (this.isPlaceholderResidentName(name)) return this.$message.error('请填写真实姓名')
                if (!Number.isFinite(balance)) return this.$message.error('请填写有效金条余额')
                try {
                    await this.write('/api/v1/residents/npc', {name, code, identity, balance, visible: true})
                    this.npcAddVisible = false
                    this.resetNpcAddForm()
                    this.$message.success('常驻居民已添加到面板')
                } catch (err) {
                    this.$message.error(err.message)
                }
            },
            async searchNpc() {
                const codeQuery = this.normalizeResidentCode(this.npcSearchCode)
                const nameQuery = String(this.npcSearchName || '').trim()
                if (!codeQuery && !nameQuery) return this.$message.warning('请输入编号或姓名后再搜索')
                try {
                    const matches = await this.searchResidents({kind: 'npc', code: codeQuery, name: nameQuery, limit: 10})
                    this.npcSearchPerformed = true
                    this.npcMatchedCandidates = matches.map((npc, index) => ({...npc, _candidateKey: `${npc.code}-${index}`}))
                    if (!matches.length) return this.$message.warning('未找到匹配的常驻居民')
                    this.$message.success(`找到 ${matches.length} 位常驻居民`)
                } catch (err) {
                    this.$message.error(err.message)
                }
            },
            isNpcInConfig(role) {
                const code = this.normalizeResidentCode(role && role.code)
                return Boolean(code && this.npcs.some(npc => this.normalizeResidentCode(npc.code) === code))
            },
            async addNpcToConfig(role) {
                const code = this.normalizeResidentCode(role && role.code)
                if (!code) return
                try {
                    await this.write('/api/v1/npc/panel', {code, visible: true})
                    this.$message.success('已加入常驻显示列表')
                } catch (err) {
                    this.$message.error(err.message || err)
                }
            },
            async removeNpcFromConfig(role) {
                if (!role || !role.code) return
                try {
                    await this.write('/api/v1/npc/panel', {code: role.code, visible: false})
                    this.$message.success('已移出常驻显示列表')
                } catch (err) {
                    this.$message.error(err.message || err)
                }
            },
            getDefaultEnterTime() {
                const times = this.enterTimeGroups.reduce((list, group) => list.concat(group.times), [])
                const now = new Date()
                const currentMinutes = now.getHours() * 60 + now.getMinutes()
                let selected = times[0]
                for (const time of times) {
                    const [hours, minutes] = time.split(':').map(Number)
                    const totalMinutes = hours * 60 + minutes
                    if (totalMinutes <= currentMinutes) selected = time
                    else break
                }
                return selected
            },
            buildTodayEnterTimeIso(timeText) { return String(timeText || this.getDefaultEnterTime()) },
            async addRole() {
                const roleCode = String(this.newRoleCode || '').trim()
                const roleName = String(this.newRoleName || '').trim()
                if (!roleCode || !roleName) return this.$message.error('请填写姓名和编号')
                if (this.isPlaceholderResidentName(roleName)) return this.$message.error('请填写真实姓名')
                if (this.newRoleStayHours <= 0) return this.$message.error('进城时长必须大于 0')
                if (!this.newRoleEnterTime) return this.$message.error('请选择进城时间')
                const defaultIdentity = this.matchedHistoricalPlayer ? (this.newRoleIdentity || '未设置') : this.buildIdentity(this.newRoleDepartment, '实习中', this.newRoleCustomDepartment)
                if (!defaultIdentity) return this.$message.error(this.newRoleDepartment === '其它' ? '请输入其它身份前缀' : '请选择部门')
                try {
                    await this.write('/api/v1/residents/player/enter', {
                        code: roleCode,
                        name: roleName,
                        balance: Number(this.newRoleBalance || 0),
                        identity: defaultIdentity,
                        enterTime: this.buildTodayEnterTimeIso(this.newRoleEnterTime),
                        stayHours: Number(this.newRoleStayHours || 0)
                    })
                    this.cancelAddRole()
                    this.$message.success('城邦居民已进城')
                } catch (err) {
                    this.$message.error(err.message)
                }
            },
            async searchRole() {
                const searchCode = this.normalizeResidentCode(this.newRoleCode)
                if (!searchCode) return this.$message.warning('请输入编号后再搜索')
                try {
                    const matches = await this.searchResidents({kind: 'player', q: searchCode, limit: 5})
                    this.roleSearchPerformed = true
                    this.roleMatchedCandidates = matches.map((role, index) => ({...role, _candidateKey: `${role.code}-${index}`}))
                    this.roleSelectedCandidateKey = ''
                    if (!matches.length) {
                        this.matchedHistoricalPlayer = null
                        this.newRoleIdentityHistory = []
                        this.newRoleCodeDisable = false
                        this.newRoleNameDisable = false
                        return this.$message.warning(`未找到编号为 ${this.newRoleCode} 的城邦居民，可手动新增`)
                    }
                    this.$message.success(`找到 ${matches.length} 位城邦居民，请选择后填入`)
                } catch (err) {
                    this.$message.error(err.message)
                }
            },
            selectRoleCandidate(candidateKey) {
                const role = this.roleMatchedCandidates.find(candidate => candidate._candidateKey === candidateKey)
                if (!role) return
                this.newRoleCodeDisable = true
                this.newRoleNameDisable = !this.isPlaceholderResidentName(role.name)
                this.matchedHistoricalPlayer = role
                this.roleSelectedCandidateKey = role._candidateKey
                this.newRoleCode = role.code
                this.newRoleName = role.name
                this.newRoleIdentity = role.identityCurrent || ''
                this.newRoleBalance = role.balance || 0
                this.newRoleIdentityHistory = (role.identityHistory || []).map(x => this.normalizeHistoryEntry(x)).filter(Boolean)
            },
            async openSummary(r) {
                this.summaryRole = r
                try {
                    this.summary = await this.api(`/api/v1/summary?code=${encodeURIComponent(r.code)}`)
                    this.summaryVisible = true
                } catch (err) {
                    this.$message.error(err.message)
                }
            },
            getLeaveTimeLabel(role) {
                if (!role.leaveTime) return '未设置'
                const leave = new Date(role.leaveTime)
                const remainMs = leave.getTime() - this.nowTimestamp
                const overdueText = remainMs <= 0 ? '（已到离城时间）' : '离城'
                return `${leave.toLocaleTimeString([], {hour: '2-digit', minute: '2-digit'})} ${overdueText}`
            },
            isSameLocalDay(value, targetTime) {
                if (!value) return false
                const date = new Date(value)
                const target = new Date(targetTime)
                if (Number.isNaN(date.getTime()) || Number.isNaN(target.getTime())) return false
                return date.getFullYear() === target.getFullYear() && date.getMonth() === target.getMonth() && date.getDate() === target.getDate()
            },
            isLeaveTimeReached(role) {
                if (!role || !role.leaveTime) return false
                return new Date(role.leaveTime).getTime() <= this.nowTimestamp
            },
            async hideResidentCard(role) {
                if (!role || !role.code) return
                const isEarlyPlayer = role.type === 'player' && !this.isLeaveTimeReached(role)
                const extraText = isEarlyPlayer ? '未到离城时间的城邦居民会保留金条流水，但会逻辑取消本次进出城记录。' : '仅隐藏显示，不影响档案与流水。'
                try {
                    await this.$confirm(`确认将 ${role.name} 从当前页面隐藏吗？${extraText}`, '确认关闭', {
                        confirmButtonText: '确认隐藏',
                        cancelButtonText: '取消',
                        type: 'warning',
                        distinguishCancelAndClose: true
                    })
                    if (role.type === 'npc') {
                        await this.write('/api/v1/npc/panel', {code: role.code, visible: false})
                    } else {
                        await this.write('/api/v1/travel/hide', {travelId: role.travelId})
                    }
                    this.$message.success('已隐藏')
                } catch (err) {
                    if (err !== 'cancel' && err !== 'close') this.$message.error(err.message || err)
                }
            },
            getResidentCardStyle(role) {
                if (!role.enterTime || !role.leaveTime) return {}
                const leave = new Date(role.leaveTime).getTime()
                const remainMs = leave - this.nowTimestamp
                const oneHourMs = 60 * 60 * 1000
                const green = {r: 103, g: 194, b: 58}
                const red = {r: 217, g: 22, b: 22}
                const lerpColor = (from, to, p) => ({r: Math.round(from.r + (to.r - from.r) * p), g: Math.round(from.g + (to.g - from.g) * p), b: Math.round(from.b + (to.b - from.b) * p)})
                const progress = Math.min(1, Math.max(0, (oneHourMs - remainMs) / oneHourMs))
                return this.getResidentCardColorStyle(lerpColor(green, red, progress))
            },
            getNpcResidentCardStyle() { return this.getResidentCardColorStyle({r: 96, g: 125, b: 139}) },
            getResidentCardColorStyle(rgb) {
                const brightness = (rgb.r * 299 + rgb.g * 587 + rgb.b * 114) / 1000
                const useLightText = brightness < 165
                return {backgroundColor: `rgb(${rgb.r},${rgb.g},${rgb.b})`, color: useLightText ? '#fffdfa' : '#1f2937', boxShadow: useLightText ? '0 6px 14px rgba(0,0,0,.15)' : '0 6px 14px rgba(31,111,209,.14)'}
            },
            ensureResidentFields(r) {
                this.currentRole = r
                if (!this.currentRole.identityCurrent) this.$set(this.currentRole, 'identityCurrent', '未设置')
                if (!this.currentRole.identityHistory) this.$set(this.currentRole, 'identityHistory', [])
                if (!this.currentRole.identityHistoryItems) this.$set(this.currentRole, 'identityHistoryItems', [])
                if (!this.currentRole.timeIncreaseLogs) this.$set(this.currentRole, 'timeIncreaseLogs', [])
                if (this.currentRole.remark == null) this.$set(this.currentRole, 'remark', '')
            },
            openGoldManage(r) {
                this.ensureResidentFields(r)
                this.operateType = 'allocate'
                this.operateAmount = 2
                this.operateRemark = ''
                this.allocateCategory = '工资'
                this.allocateCustomReason = ''
                this.operateVisible = true
            },
            openIdentityManage(r) {
                this.ensureResidentFields(r)
                const parsed = this.parseIdentityText(this.currentRole.identityCurrent)
                this.identityDepartment = parsed.department
                this.identityStage = parsed.stage
                this.identityCustomDepartment = parsed.customDepartment || ''
                this.identityVisible = true
            },
            openTimeManage(r) {
                this.ensureResidentFields(r)
                this.timeAddHours = 0.5
                this.timeVisible = true
            },
            async submitIdentity() {
                if (!this.identityDepartment) return this.$message.warning('请选择部门')
                if (this.identityDepartment !== '自由人' && !this.identityStage) return this.$message.warning('请选择状态')
                if (this.identityDepartment === '其它' && !String(this.identityCustomDepartment || '').trim()) return this.$message.warning('请输入其它身份前缀')
                const identityText = this.buildIdentity(this.identityDepartment, this.identityStage, this.identityCustomDepartment)
                const code = this.normalizeResidentCode(this.currentRole && this.currentRole.code)
                try {
                    await this.write('/api/v1/identity', {code: this.currentRole.code, identity: identityText})
                    await this.refreshMaintenanceSelectedByCode(code)
                    this.identityVisible = false
                    this.$message.success('身份已更新')
                } catch (err) {
                    this.$message.error(err.message)
                }
            },
            async deleteIdentityHistory(idx) {
                if (!this.currentRole || !Array.isArray(this.currentRole.identityHistoryItems)) return
                const item = this.currentRole.identityHistoryItems[idx]
                if (!item || !item.id) return
                try {
                    await this.$confirm('确认删除这条历史身份记录吗？', '删除确认', {type: 'warning'})
                    await this.write(`/api/v1/identity/history/${item.id}`, {}, {method: 'DELETE'})
                    this.$message.success('历史身份记录已删除')
                } catch (err) {
                    if (err !== 'cancel' && err !== 'close') this.$message.error(err.message || err)
                }
            },
            async submitTimeManage() {
                if (!this.currentRole || !this.currentRole.travelId) return this.$message.warning('该居民暂无进城记录')
                const adjustHours = Number(this.timeAddHours)
                if (!Number.isFinite(adjustHours) || adjustHours === 0) return this.$message.warning('请输入有效的调整时长')
                const absHours = Math.abs(adjustHours)
                const withinHalfHourStep = (absHours * 10) % 5 === 0
                const withinRecommendedRange = absHours >= 0.5 && absHours <= 10
                if (!withinHalfHourStep || !withinRecommendedRange) {
                    const reasons = []
                    if (!withinHalfHourStep) reasons.push('时长需为 0.5 小时的倍数')
                    if (!withinRecommendedRange) reasons.push('建议单次调整范围为 -10 ~ -0.5 或 0.5 ~ 10 小时')
                    try {
                        await this.$confirm(`${reasons.join('，')}。是否确认继续？`, '超出默认范围确认', {confirmButtonText: '继续操作', cancelButtonText: '取消', type: 'warning', distinguishCancelAndClose: true})
                    } catch (err) {
                        return
                    }
                }
                try {
                    await this.write('/api/v1/travel/extensions', {travelId: this.currentRole.travelId, hours: adjustHours})
                    this.timeVisible = false
                    this.$message.success('进城时长已调整')
                } catch (err) {
                    this.$message.error(err.message)
                }
            },
            applyTimeIncrease(addHours) { this.timeAddHours = addHours; this.submitTimeManage() },
            formatDateTime(v) {
                if (!v) return '未设置'
                return new Date(v).toLocaleString()
            },
            openOperate(r) { this.openGoldManage(r) },
            async submitOperate() {
                const type = this.operateType
                const amount = Number(this.operateAmount || 0)
                if (!this.currentRole) return
                const code = this.normalizeResidentCode(this.currentRole.code)
                if (!Number.isFinite(amount) || amount <= 0) return this.$message.warning('请输入有效数量')
                try {
                    await this.write('/api/v1/gold/records', {
                        code: this.currentRole.code,
                        type,
                        amount,
                        remark: this.operateRemark,
                        allocateCategory: this.allocateCategory,
                        allocateReason: this.allocateCustomReason
                    })
                    await this.refreshMaintenanceSelectedByCode(code)
                    this.operateVisible = false
                    this.$message.success('金条业务已记录')
                } catch (err) {
                    this.$message.error(err.message)
                }
            },
            async voidRecord(r) {
                try {
                    await this.$confirm('该操作将作废这条记录，并回退对应金额，是否继续？', '确认作废', {confirmButtonText: '确定作废', cancelButtonText: '取消', type: 'warning', distinguishCancelAndClose: true})
                    await this.write(`/api/v1/gold/records/${r.id}/void`, {})
                    this.$message.success('流水已作废')
                } catch (err) {
                    if (err !== 'cancel' && err !== 'close') this.$message.error(err.message || err)
                }
            },
            openCloudSync() {
                try {
                    const savedBaseUrl = window.localStorage && window.localStorage.getItem('ncpt-cloud-admin-base-url')
                    if (!this.cloudSyncAdminBaseUrl && String(savedBaseUrl || '').trim()) this.cloudSyncAdminBaseUrl = savedBaseUrl
                } catch (err) {}
                if (!this.cloudSyncAdminBaseUrl) this.cloudSyncAdminBaseUrl = defaultCloudSyncAdminBaseUrl
                this.cloudSyncPassword = ''
                this.cloudSyncResult = null
                this.cloudSyncError = ''
                this.cloudSyncVisible = true
            },
            resetCloudSyncToken() {
                this.cloudSyncToken = ''
                this.cloudSyncPassword = ''
                this.cloudSyncResult = null
                this.cloudSyncError = ''
            },
            async submitCloudSync() {
                const adminBaseUrl = String(this.cloudSyncAdminBaseUrl || '').trim()
                const password = String(this.cloudSyncPassword || '')
                if (!adminBaseUrl) return this.$message.warning('请输入 admin 地址')
                if (!password) return this.$message.warning('请输入上传密码')
                this.cloudSyncLoading = true
                this.cloudSyncError = ''
                this.cloudSyncResult = null
                try {
                    try {
                        if (window.localStorage) window.localStorage.setItem('ncpt-cloud-admin-base-url', adminBaseUrl)
                    } catch (err) {}
                    const result = await this.api('/api/v1/cloud/ncpt/sync', {
                        method: 'POST',
                        body: {adminBaseUrl, password}
                    })
                    this.cloudSyncResult = result || {}
                    this.cloudSyncPassword = ''
                    this.$message.success('云端上传完成')
                } catch (err) {
                    this.cloudSyncError = err.message || String(err)
                    this.$message.error(this.cloudSyncError)
                } finally {
                    this.cloudSyncLoading = false
                }
            },
            balanceOperateRemarkQuerySearch(queryString, cb) {
                const suggestions = ['每日工资', '任务奖励', '罚没违规物资', '税收', '来自人事的爱']
                cb(suggestions.filter(item => item.includes(queryString)).map(item => ({value: item})))
            },
            findRoleByCode(code) {
                const normalized = this.normalizeResidentCode(code)
                return this.roles.find(role => this.normalizeResidentCode(role.code) === normalized)
            },
            findResidentByCode(code) {
                const normalized = this.normalizeResidentCode(code)
                return this.roles.find(role => this.normalizeResidentCode(role.code) === normalized)
            }
        },
        async mounted() {
            try {
                await this.load()
            } catch (err) {
                this.$message.error(`加载本地 SQLite 数据失败：${err.message || err}`)
            }
            this.nowTicker = setInterval(() => { this.nowTimestamp = Date.now() }, 30000)
        },
        beforeDestroy() {
            if (this.nowTicker) clearInterval(this.nowTicker)
        }
    })
})()
