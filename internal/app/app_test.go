package app

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

type testEnv struct {
	store  *Store
	server *httptest.Server
	csrf   string
	now    time.Time
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	env := &testEnv{now: time.Date(2026, 6, 18, 15, 0, 0, 0, loc)}
	store, err := OpenStore(context.Background(), Config{
		DataDir: t.TempDir(),
		Now: func() time.Time {
			return env.now
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	env.store = store
	t.Cleanup(func() { _ = store.Close() })
	srv := NewServer(store, fstest.MapFS{
		"frontend/index.html": &fstest.MapFile{Data: []byte("<html></html>")},
	})
	env.server = httptest.NewServer(srv.Handler())
	t.Cleanup(env.server.Close)
	var boot AppState
	env.getJSON(t, "/api/v1/bootstrap", &boot)
	env.csrf = boot.CSRFToken
	if env.csrf == "" {
		t.Fatal("bootstrap did not return csrf token")
	}
	return env
}

func (e *testEnv) getJSON(t *testing.T, path string, out any) {
	t.Helper()
	resp, err := http.Get(e.server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status=%d", path, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}

func (e *testEnv) writeJSON(t *testing.T, method, path string, payload any, key string, out any) int {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, e.server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-NCFMS-CSRF", e.csrf)
	req.Header.Set("Idempotency-Key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func (e *testEnv) openCity(t *testing.T, operator string) AppState {
	t.Helper()
	var state AppState
	status := e.writeJSON(t, http.MethodPost, "/api/v1/city/open", map[string]any{"operator": operator, "openedAt": e.now.Format(time.RFC3339)}, "open-"+operator, &state)
	if status != http.StatusOK {
		t.Fatalf("open city status=%d", status)
	}
	return state
}

func (e *testEnv) closeCity(t *testing.T, key string) AppState {
	t.Helper()
	var state AppState
	status := e.writeJSON(t, http.MethodPost, "/api/v1/city/close", map[string]any{}, key, &state)
	if status != http.StatusOK {
		t.Fatalf("close city status=%d", status)
	}
	return state
}

func (e *testEnv) enterPlayer(t *testing.T, key, code, name string, balance int64) AppState {
	t.Helper()
	var state AppState
	status := e.writeJSON(t, http.MethodPost, "/api/v1/residents/player/enter", map[string]any{
		"code": code, "name": name, "balance": balance, "identity": "城防部实习中", "enterTime": "14:30", "stayHours": 2,
	}, key, &state)
	if status != http.StatusOK {
		t.Fatalf("enter player status=%d", status)
	}
	return state
}

func (e *testEnv) gold(t *testing.T, key, code, typ string, amount int64) AppState {
	t.Helper()
	var state AppState
	status := e.writeJSON(t, http.MethodPost, "/api/v1/gold/records", map[string]any{
		"code": code, "type": typ, "amount": amount, "allocateCategory": "工资",
	}, key, &state)
	if status != http.StatusOK {
		t.Fatalf("gold %s status=%d", typ, status)
	}
	return state
}

func (e *testEnv) searchResidents(t *testing.T, kind, code, name string, limit int) []RoleDTO {
	t.Helper()
	var out struct {
		Residents []RoleDTO `json:"residents"`
	}
	path := "/api/v1/residents/search?kind=" + url.QueryEscape(kind) + "&limit=" + itoa(int64(limit))
	if code != "" {
		path += "&code=" + url.QueryEscape(code)
	}
	if name != "" {
		path += "&name=" + url.QueryEscape(name)
	}
	e.getJSON(t, path, &out)
	return out.Residents
}

func (e *testEnv) searchResidentsQuery(t *testing.T, kind, query string, limit int) []RoleDTO {
	t.Helper()
	var out struct {
		Residents []RoleDTO `json:"residents"`
	}
	path := "/api/v1/residents/search?kind=" + url.QueryEscape(kind) + "&q=" + url.QueryEscape(query) + "&limit=" + itoa(int64(limit))
	e.getJSON(t, path, &out)
	return out.Residents
}

func TestResidentCodeOpaqueAndGlobalUnique(t *testing.T) {
	env := newTestEnv(t)
	env.openCity(t, "alice")
	state := env.enterPlayer(t, "enter-01234", "01234", "零号", 0)
	if got := findPlayer(t, state, "01234").Code; got != "01234" {
		t.Fatalf("leading zero code changed: %q", got)
	}
	var npcState AppState
	status := env.writeJSON(t, http.MethodPost, "/api/v1/residents/npc", map[string]any{
		"code": "1234", "name": "一号", "identity": "保安部正式员工", "balance": 1, "visible": true,
	}, "npc-1234", &npcState)
	if status != http.StatusOK {
		t.Fatalf("expected distinct 1234 to be allowed, status=%d", status)
	}
	var errBody map[string]any
	status = env.writeJSON(t, http.MethodPost, "/api/v1/residents/npc", map[string]any{
		"code": " 1234 ", "name": "duplicate", "identity": "guard", "balance": 2, "visible": true,
	}, "npc-1234-duplicate", &errBody)
	if status != http.StatusConflict {
		t.Fatalf("expected duplicate npc code conflict, got %d body=%v", status, errBody)
	}

	status = env.writeJSON(t, http.MethodPost, "/api/v1/residents/npc", map[string]any{
		"code": "01234", "name": "冲突", "identity": "保安部正式员工", "balance": 1, "visible": true,
	}, "npc-conflict", &errBody)
	if status != http.StatusConflict {
		t.Fatalf("expected global uniqueness conflict, got %d body=%v", status, errBody)
	}
	playerMatches := env.searchResidents(t, KindPlayer, "01234", "", 5)
	if len(playerMatches) != 1 || playerMatches[0].Code != "01234" {
		t.Fatalf("player exact search for 01234 = %#v", playerMatches)
	}
	playerMatches = env.searchResidents(t, KindPlayer, "1234", "", 5)
	if len(playerMatches) != 0 {
		t.Fatalf("player exact search matched 1234 against 01234: %#v", playerMatches)
	}
	playerMatches = env.searchResidentsQuery(t, KindPlayer, "123", 5)
	if len(playerMatches) != 1 || playerMatches[0].Code != "01234" {
		t.Fatalf("player fuzzy search for 123 should find 01234: %#v", playerMatches)
	}
	npcMatches := env.searchResidents(t, KindNPC, "1234", "", 5)
	if len(npcMatches) != 1 || npcMatches[0].Code != "1234" {
		t.Fatalf("npc exact search for 1234 = %#v", npcMatches)
	}
	npcMatches = env.searchResidents(t, KindNPC, "", "号", 5)
	if len(npcMatches) != 1 || npcMatches[0].Code != "1234" {
		t.Fatalf("npc fuzzy name search for 号 = %#v", npcMatches)
	}
}

func TestNPCPanelConfigurationWorksWithoutOpenCity(t *testing.T) {
	env := newTestEnv(t)

	var state AppState
	status := env.writeJSON(t, http.MethodPost, "/api/v1/residents/npc", map[string]any{
		"code": " 01234 ", "name": "常驻零号", "identity": "保安部正式员工", "balance": 7, "visible": true,
	}, "npc-config-add", &state)
	if status != http.StatusOK {
		t.Fatalf("add npc without open city status=%d", status)
	}
	if state.Session.CurrentSession != nil {
		t.Fatalf("npc config opened a city session: %#v", state.Session.CurrentSession)
	}
	npc := findNPC(t, state, "01234")
	if npc.Code != "01234" || npc.Name != "常驻零号" {
		t.Fatalf("configured npc = %#v", npc)
	}

	matches := env.searchResidents(t, KindNPC, "01234", "", 5)
	if len(matches) != 1 || matches[0].Code != "01234" {
		t.Fatalf("npc exact search for 01234 = %#v", matches)
	}
	matches = env.searchResidents(t, KindNPC, "1234", "", 5)
	if len(matches) != 0 {
		t.Fatalf("npc exact search matched 1234 against 01234: %#v", matches)
	}

	status = env.writeJSON(t, http.MethodPost, "/api/v1/npc/panel", map[string]any{"code": " 01234 ", "visible": false}, "npc-config-hide", &state)
	if status != http.StatusOK {
		t.Fatalf("hide npc without open city status=%d", status)
	}
	if hasNPC(state, "01234") {
		t.Fatalf("hidden npc still visible in returned state: %#v", state.Session.Roles)
	}

	var boot AppState
	env.getJSON(t, "/api/v1/bootstrap", &boot)
	if hasNPC(boot, "01234") {
		t.Fatalf("hidden npc still visible after bootstrap: %#v", boot.Session.Roles)
	}

	status = env.writeJSON(t, http.MethodPost, "/api/v1/npc/panel", map[string]any{"code": "01234", "visible": true}, "npc-config-show", &state)
	if status != http.StatusOK {
		t.Fatalf("show npc without open city status=%d", status)
	}
	findNPC(t, state, "01234")
}

func TestResidentMaintenanceWorksWithoutOpenCity(t *testing.T) {
	env := newTestEnv(t)
	env.openCity(t, "op")
	env.enterPlayer(t, "enter-maintenance", "01234", "旧名", 10)
	env.closeCity(t, "close-before-maintenance")

	matches := env.searchResidents(t, "", "01234", "", 5)
	if len(matches) != 1 || matches[0].Code != "01234" {
		t.Fatalf("closed resident exact search = %#v", matches)
	}

	state := env.gold(t, "offline-gold", "01234", GoldIn, 5)
	if state.Session.CurrentSession != nil {
		t.Fatalf("offline gold opened a city session: %#v", state.Session.CurrentSession)
	}
	if len(state.Session.Records) != 0 {
		t.Fatalf("offline state should not show session records: %#v", state.Session.Records)
	}
	matches = env.searchResidents(t, "", "01234", "", 5)
	if len(matches) != 1 || matches[0].Balance != 15 {
		t.Fatalf("offline gold did not update searchable balance: %#v", matches)
	}

	status := env.writeJSON(t, http.MethodPost, "/api/v1/identity", map[string]any{"code": "01234", "identity": "保安部正式员工"}, "offline-identity", &state)
	if status != http.StatusOK {
		t.Fatalf("offline identity status=%d", status)
	}
	matches = env.searchResidents(t, "", "01234", "", 5)
	if len(matches) != 1 || matches[0].IdentityCurrent != "保安部正式员工" || len(matches[0].IdentityHistory) == 0 {
		t.Fatalf("offline identity not searchable with history: %#v", matches)
	}

	status = env.writeJSON(t, http.MethodPatch, "/api/v1/residents/01234/profile", map[string]any{"code": " 001234 ", "name": "新名", "remark": "手工维护"}, "offline-profile", &state)
	if status != http.StatusOK {
		t.Fatalf("offline profile status=%d", status)
	}
	matches = env.searchResidents(t, "", "01234", "", 5)
	if len(matches) != 0 {
		t.Fatalf("old code still matched after renumber: %#v", matches)
	}
	matches = env.searchResidents(t, "", "1234", "", 5)
	if len(matches) != 0 {
		t.Fatalf("exact search treated 001234 as 1234: %#v", matches)
	}
	matches = env.searchResidents(t, "", "001234", "", 5)
	if len(matches) != 1 {
		t.Fatalf("new code search = %#v", matches)
	}
	updated := matches[0]
	if updated.Code != "001234" || updated.Name != "新名" || updated.Balance != 15 {
		t.Fatalf("updated resident = %#v", updated)
	}
	if !strings.Contains(updated.Remark, "手工维护") || !strings.Contains(updated.Remark, "原姓名：旧名") || !strings.Contains(updated.Remark, "原编号：01234") {
		t.Fatalf("profile remark missing history notes: %q", updated.Remark)
	}

	status = env.writeJSON(t, http.MethodPost, "/api/v1/residents/npc", map[string]any{"code": "999", "name": "冲突编号", "identity": "保安部正式员工", "balance": 0, "visible": true}, "maintenance-conflict-seed", &state)
	if status != http.StatusOK {
		t.Fatalf("seed conflict resident status=%d", status)
	}
	var errBody map[string]any
	status = env.writeJSON(t, http.MethodPatch, "/api/v1/residents/001234/profile", map[string]any{"code": "999", "name": "新名", "remark": "手工维护"}, "maintenance-conflict", &errBody)
	if status != http.StatusConflict {
		t.Fatalf("renumber conflict status=%d body=%v", status, errBody)
	}

	exported, err := env.store.ExportData(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	dbRow := findRow(t, exported, "数据库", "编号", "001234")
	if dbRow["编号"] != "001234" || dbRow["姓名"] != "新名" {
		t.Fatalf("database export row = %#v", dbRow)
	}
	goldRow := firstRow(t, exported, "金条流水")
	if goldRow["编号"] != "001234" || goldRow["姓名"] != "旧名" || goldRow["操作员"] != "" {
		t.Fatalf("gold export row = %#v", goldRow)
	}
	identityRows := sheetRows(t, exported, "身份历史")
	foundIdentity := false
	for _, row := range identityRows {
		if row["编号"] == "001234" && row["身份"] == "保安部正式员工" {
			foundIdentity = true
		}
		if row["编号"] == "01234" {
			t.Fatalf("identity history kept old code after renumber: %#v", identityRows)
		}
	}
	if !foundIdentity {
		t.Fatalf("identity export missing new-code identity: %#v", identityRows)
	}
	travelRow := firstRow(t, exported, "玩家进出城记录")
	if travelRow["编号"] != "001234" {
		t.Fatalf("travel export row kept old code: %#v", travelRow)
	}
}
func TestOpenCityDoesNotClearHistoryAndTracksOperator(t *testing.T) {
	env := newTestEnv(t)
	env.openCity(t, "old-op")
	env.enterPlayer(t, "enter-001", "001", "居民", 7)
	env.now = env.now.Add(time.Hour)
	state := env.openCity(t, "new-op")
	if state.Operator != "new-op" {
		t.Fatalf("operator = %q", state.Operator)
	}
	if len(state.HistoricalPlayers) != 0 {
		t.Fatalf("interactive state should not include historical players: %#v", state.HistoricalPlayers)
	}
	matches := env.searchResidents(t, KindPlayer, "001", "", 5)
	if len(matches) != 1 || matches[0].Code != "001" {
		t.Fatalf("historical player not found through search: %#v", matches)
	}
	if len(state.Session.Roles) != 0 {
		t.Fatalf("new session should not show previous travel cards: %#v", state.Session.Roles)
	}
}

func TestOpenCityUsesProvidedTimeAndRejectsDuplicate(t *testing.T) {
	env := newTestEnv(t)
	openedAt := env.now.Format(time.RFC3339)
	state := env.openCity(t, "gate-op")
	if state.Session.CurrentSession == nil || state.Session.CurrentSession.OpenedAt != openedAt {
		t.Fatalf("current session openedAt=%#v want %s", state.Session.CurrentSession, openedAt)
	}

	var errBody map[string]any
	status := env.writeJSON(t, http.MethodPost, "/api/v1/city/open", map[string]any{"operator": "dup-op", "openedAt": openedAt}, "open-duplicate", &errBody)
	if status != http.StatusConflict {
		t.Fatalf("duplicate open city time status=%d body=%v", status, errBody)
	}
}

func TestEnterPlayerTimeUsesCurrentOpenCityDate(t *testing.T) {
	env := newTestEnv(t)
	env.now = time.Date(2026, 6, 19, 9, 0, 0, 0, env.now.Location())
	env.openCity(t, "date-op")
	env.now = env.now.Add(24 * time.Hour)

	state := env.enterPlayer(t, "enter-open-date", "D-001", "日期居民", 1)
	role := findPlayer(t, state, "D-001")
	if got := env.store.formatDisplayTime(role.EnterTime); got != "2026/6/19 14:30:00" {
		t.Fatalf("enter time date = %q", got)
	}
}

func TestExtendTravelAllowsNegativeAdjustmentWithMinimumStay(t *testing.T) {
	env := newTestEnv(t)
	env.openCity(t, "time-op")
	state := env.enterPlayer(t, "enter-time-adjust", "T-001", "时长居民", 0)
	role := findPlayer(t, state, "T-001")

	env.now = env.now.Add(10 * time.Minute)
	status := env.writeJSON(t, http.MethodPost, "/api/v1/travel/extensions", map[string]any{"travelId": role.TravelID, "hours": 1.0}, "extend-positive", &state)
	if status != http.StatusOK {
		t.Fatalf("positive extend status=%d", status)
	}
	role = findPlayer(t, state, "T-001")
	if got := role.StayHours; got != 3 {
		t.Fatalf("stay hours after positive adjust = %v, want 3", got)
	}
	if got := env.store.formatDisplayTime(role.LeaveTime); got != "2026/6/18 17:30:00" {
		t.Fatalf("leave time after positive adjust = %q", got)
	}

	env.now = env.now.Add(10 * time.Minute)
	status = env.writeJSON(t, http.MethodPost, "/api/v1/travel/extensions", map[string]any{"travelId": role.TravelID, "hours": -1.5}, "extend-negative", &state)
	if status != http.StatusOK {
		t.Fatalf("negative adjust status=%d", status)
	}
	role = findPlayer(t, state, "T-001")
	if got := role.StayHours; got != 1.5 {
		t.Fatalf("stay hours after negative adjust = %v, want 1.5", got)
	}
	if got := env.store.formatDisplayTime(role.LeaveTime); got != "2026/6/18 16:00:00" {
		t.Fatalf("leave time after negative adjust = %q", got)
	}
	if len(role.TimeIncreaseLog) < 2 || role.TimeIncreaseLog[0].Minutes != -90 || role.TimeIncreaseLog[1].Minutes != 60 {
		t.Fatalf("time adjustment logs = %#v", role.TimeIncreaseLog)
	}

	var errBody map[string]any
	status = env.writeJSON(t, http.MethodPost, "/api/v1/travel/extensions", map[string]any{"travelId": role.TravelID, "hours": -1.25}, "extend-too-short", &errBody)
	if status != http.StatusBadRequest {
		t.Fatalf("too-short adjust status=%d body=%v", status, errBody)
	}
	status = env.writeJSON(t, http.MethodPost, "/api/v1/travel/extensions", map[string]any{"travelId": role.TravelID, "hours": 0}, "extend-zero", &errBody)
	if status != http.StatusBadRequest {
		t.Fatalf("zero adjust status=%d body=%v", status, errBody)
	}
	current, err := env.store.State(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	role = findPlayer(t, *current, "T-001")
	if got := role.StayHours; got != 1.5 {
		t.Fatalf("stay hours changed after rejected adjust = %v, want 1.5", got)
	}
}

func TestNewSchemaUsesOpenedAtSessionKey(t *testing.T) {
	env := newTestEnv(t)
	assertColumnPresence(t, env.store, "city_sessions", "id", false)
	assertColumnPresence(t, env.store, "city_sessions", "opened_at", true)
	assertColumnPresence(t, env.store, "travel_records", "session_id", false)
	assertColumnPresence(t, env.store, "travel_records", "session_opened_at", true)
}

func TestOpenStoreRejectsLegacySessionSchema(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", sqliteDSN(filepath.Join(dir, "ncfms.db")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE city_sessions (
        id INTEGER PRIMARY KEY AUTOINCREMENT,
        opened_at TEXT NOT NULL,
        operator TEXT NOT NULL
    )`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = OpenStore(context.Background(), Config{DataDir: dir})
	if err == nil || !strings.Contains(err.Error(), "unsupported legacy database schema") {
		t.Fatalf("expected legacy schema error, got %v", err)
	}
}
func TestInteractiveStateAndGoldRecordsAreSessionScoped(t *testing.T) {
	env := newTestEnv(t)
	env.openCity(t, "old-op")
	env.enterPlayer(t, "enter-old", "OLD-001", "旧居民", 1)
	env.gold(t, "gold-old", "OLD-001", GoldIn, 3)
	env.now = env.now.Add(time.Hour)

	state := env.openCity(t, "new-op")
	if len(state.HistoricalPlayers) != 0 || len(state.Session.HistoricalNPCs) != 0 {
		t.Fatalf("interactive state leaked historical residents: players=%#v npcs=%#v", state.HistoricalPlayers, state.Session.HistoricalNPCs)
	}
	if len(state.Session.Records) != 0 {
		t.Fatalf("new session leaked old gold records: %#v", state.Session.Records)
	}
	if got := state.Stats.DailyExpense; got != 0 {
		t.Fatalf("new session daily expense included old session records: %d", got)
	}
	if got := state.Stats.TodayEntered; got != 0 {
		t.Fatalf("new session today entered included old session records: %d", got)
	}
	if got := state.Stats.CurrentInCity; got != 0 {
		t.Fatalf("new session current in city included old session records: %d", got)
	}

	state = env.enterPlayer(t, "enter-current", "CUR-001", "当前居民", 2)
	if got := state.Stats.TodayEntered; got != 1 {
		t.Fatalf("current session today entered = %d, want 1", got)
	}
	if got := state.Stats.CurrentInCity; got != 1 {
		t.Fatalf("current session current in city = %d, want 1", got)
	}
	state = env.gold(t, "gold-current", "CUR-001", GoldOut, 1)
	if len(state.Session.Records) != 1 || state.Session.Records[0].Code != "CUR-001" {
		t.Fatalf("gold write returned records outside current session: %#v", state.Session.Records)
	}
	if got := state.Stats.DailyExpense; got != 1 {
		t.Fatalf("current session daily expense = %d, want 1", got)
	}
	if got := state.Stats.TodayEntered; got != 1 {
		t.Fatalf("gold write returned today entered = %d, want 1", got)
	}
	if got := state.Stats.CurrentInCity; got != 1 {
		t.Fatalf("gold write returned current in city = %d, want 1", got)
	}

	var boot AppState
	env.getJSON(t, "/api/v1/bootstrap", &boot)
	if len(boot.HistoricalPlayers) != 0 || len(boot.Session.HistoricalNPCs) != 0 {
		t.Fatalf("bootstrap leaked historical residents: players=%#v npcs=%#v", boot.HistoricalPlayers, boot.Session.HistoricalNPCs)
	}
	if len(boot.Session.Records) != 1 || boot.Session.Records[0].Code != "CUR-001" {
		t.Fatalf("bootstrap returned non-current gold records: %#v", boot.Session.Records)
	}
	if got := boot.Stats.DailyExpense; got != 1 {
		t.Fatalf("bootstrap daily expense = %d, want 1", got)
	}
	if got := boot.Stats.TodayEntered; got != 1 {
		t.Fatalf("bootstrap today entered = %d, want 1", got)
	}
	if got := boot.Stats.CurrentInCity; got != 1 {
		t.Fatalf("bootstrap current in city = %d, want 1", got)
	}

	exported, err := env.store.ExportData(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := len(sheetRows(t, exported, "金条流水")); got != 2 {
		t.Fatalf("export should keep full gold history, rows=%d", got)
	}
}

func TestCloseCityClearsPanelAndKeepsTravelExport(t *testing.T) {
	env := newTestEnv(t)
	env.openCity(t, "closer")
	state := env.enterPlayer(t, "enter-close", "C-001", "闭城居民", 1)
	if got := state.Stats.TodayEntered; got != 1 {
		t.Fatalf("today entered before close = %d", got)
	}
	if got := state.Stats.CurrentInCity; got != 1 {
		t.Fatalf("current in city before close = %d", got)
	}
	env.now = env.now.Add(30 * time.Minute)
	state = env.closeCity(t, "close-city")
	if state.Session.CurrentSession != nil {
		t.Fatalf("current session still active after close: %#v", state.Session.CurrentSession)
	}
	for _, role := range state.Session.Roles {
		if role.Type == KindPlayer {
			t.Fatalf("player card still visible after close: %#v", role)
		}
	}
	if got := state.Stats.CurrentInCity; got != 0 {
		t.Fatalf("current in city after close = %d", got)
	}
	if got := state.Stats.TodayEntered; got != 0 {
		t.Fatalf("today entered after close = %d", got)
	}
	if len(state.HistoricalPlayers) != 0 {
		t.Fatalf("closed interactive state should not include historical players: %#v", state.HistoricalPlayers)
	}
	matches := env.searchResidents(t, KindPlayer, "C-001", "", 5)
	if len(matches) != 1 || matches[0].Code != "C-001" {
		t.Fatalf("closed resident not found through search: %#v", matches)
	}
	exported, err := env.store.ExportData(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sessionRows := sheetRows(t, exported, "开城记录")
	if len(sessionRows) != 1 {
		t.Fatalf("session export rows=%d", len(sessionRows))
	}
	if got := sessionRows[0]["关城时间"]; got != "2026/6/18 15:30:00" {
		t.Fatalf("close time export = %q", got)
	}
	if got := len(sheetRows(t, exported, "玩家进出城记录")); got != 1 {
		t.Fatalf("travel export rows=%d", got)
	}
	if got := len(sheetRows(t, exported, "已取消进出城记录")); got != 0 {
		t.Fatalf("canceled travel export rows=%d", got)
	}
	var errBody map[string]any
	status := env.writeJSON(t, http.MethodPost, "/api/v1/city/close", map[string]any{}, "close-again", &errBody)
	if status != http.StatusBadRequest {
		t.Fatalf("close without active session status=%d body=%v", status, errBody)
	}
}

func TestGoldRulesVoidAndNegativeBalance(t *testing.T) {
	env := newTestEnv(t)
	env.openCity(t, "cashier")
	env.enterPlayer(t, "enter-gold", "G-001", "账房", 0)
	state := env.gold(t, "gold-out", "G-001", GoldOut, 5)
	if got := findPlayer(t, state, "G-001").Balance; got != -5 {
		t.Fatalf("negative balance not allowed or wrong: %d", got)
	}
	state = env.gold(t, "gold-in", "G-001", GoldIn, 2)
	state = env.gold(t, "gold-forfeit", "G-001", GoldForfeit, 4)
	state = env.gold(t, "gold-allocate", "G-001", GoldAllocate, 6)
	if got := findPlayer(t, state, "G-001").Balance; got != -7 {
		t.Fatalf("balance after four operations = %d", got)
	}
	if got := state.Stats.DailyExpense; got != 9 {
		t.Fatalf("daily expense = %d, want 9", got)
	}
	outID := findRecordID(t, state, GoldOut)
	status := env.writeJSON(t, http.MethodPost, "/api/v1/gold/records/"+itoa(outID)+"/void", map[string]any{}, "void-out", &state)
	if status != http.StatusOK {
		t.Fatalf("void status=%d", status)
	}
	if got := findPlayer(t, state, "G-001").Balance; got != -2 {
		t.Fatalf("balance after void = %d", got)
	}
	status = env.writeJSON(t, http.MethodPost, "/api/v1/gold/records/"+itoa(outID)+"/void", map[string]any{}, "void-out-again", &state)
	if status != http.StatusOK {
		t.Fatalf("second void status=%d", status)
	}
	if got := findPlayer(t, state, "G-001").Balance; got != -2 {
		t.Fatalf("second void reverted twice, balance=%d", got)
	}
}

func TestIdentityRenameSnapshotsRemainStable(t *testing.T) {
	env := newTestEnv(t)
	env.openCity(t, "op")
	state := env.enterPlayer(t, "enter-snapshot", "S-001", "旧名", 10)
	var setState AppState
	status := env.writeJSON(t, http.MethodPost, "/api/v1/identity", map[string]any{"code": "S-001", "identity": "保安部正式员工"}, "identity-set", &setState)
	if status != http.StatusOK {
		t.Fatalf("set identity status=%d", status)
	}
	state = env.gold(t, "gold-snapshot", "S-001", GoldIn, 1)
	status = env.writeJSON(t, http.MethodPatch, "/api/v1/residents/S-001/profile", map[string]any{"name": "新名", "remark": "改名"}, "rename", &state)
	if status != http.StatusOK {
		t.Fatalf("rename status=%d", status)
	}
	if findPlayer(t, state, "S-001").Name != "新名" {
		t.Fatalf("resident name not updated")
	}
	exported, err := env.store.ExportData(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	goldRow := firstRow(t, exported, "金条流水")
	if goldRow["姓名"] != "旧名" {
		t.Fatalf("gold snapshot changed after rename: %#v", goldRow)
	}
	identityRows := sheetRows(t, exported, "身份历史")
	foundOldName := false
	for _, row := range identityRows {
		if row["编号"] == "S-001" && row["身份"] == "保安部正式员工" && row["姓名快照"] == "旧名" {
			foundOldName = true
		}
	}
	if !foundOldName {
		t.Fatalf("identity history did not preserve old name snapshot: %#v", identityRows)
	}
}

func TestMultipleEnterAndEarlyHideCancelsTravelOnly(t *testing.T) {
	env := newTestEnv(t)
	env.openCity(t, "op-a")
	state := env.enterPlayer(t, "enter-hide", "H-001", "旅人", 3)
	travelID := findPlayer(t, state, "H-001").TravelID
	state = env.gold(t, "gold-before-hide", "H-001", GoldIn, 5)
	status := env.writeJSON(t, http.MethodPost, "/api/v1/travel/hide", map[string]any{"travelId": travelID}, "hide-early", &state)
	if status != http.StatusOK {
		t.Fatalf("hide status=%d", status)
	}
	if len(state.Session.Records) != 1 || state.Session.Records[0].Code != "H-001" {
		t.Fatalf("current session gold record missing after hide: %#v", state.Session.Records)
	}
	var summary map[string]any
	env.getJSON(t, "/api/v1/summary?code=H-001", &summary)
	if got := int64(summary["balance"].(float64)); got != 8 {
		t.Fatalf("gold balance changed by hide: %d", got)
	}
	exported, err := env.store.ExportData(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := len(sheetRows(t, exported, "玩家进出城记录")); got != 0 {
		t.Fatalf("canceled travel leaked into normal export: %d", got)
	}
	if got := len(sheetRows(t, exported, "已取消进出城记录")); got != 1 {
		t.Fatalf("canceled travel sheet rows = %d", got)
	}
	env.now = env.now.Add(24 * time.Hour)
	env.openCity(t, "op-b")
	state = env.enterPlayer(t, "enter-again", "H-001", "旅人", 0)
	if got := findPlayer(t, state, "H-001").TravelID; got == travelID || got == 0 {
		t.Fatalf("new entry did not create independent travel record: old=%d new=%d", travelID, got)
	}
}

func TestCSRFAndIdempotency(t *testing.T) {
	env := newTestEnv(t)
	req, err := http.NewRequest(http.MethodPost, env.server.URL+"/api/v1/city/open", strings.NewReader(`{"operator":"bad"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "missing-csrf")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d", resp.StatusCode)
	}

	key := "same-open-key"
	var first AppState
	status := env.writeJSON(t, http.MethodPost, "/api/v1/city/open", map[string]any{"operator": "idem", "openedAt": env.now.Format(time.RFC3339)}, key, &first)
	if status != http.StatusOK {
		t.Fatalf("first idempotent write status=%d", status)
	}
	var second AppState
	status = env.writeJSON(t, http.MethodPost, "/api/v1/city/open", map[string]any{"operator": "idem", "openedAt": env.now.Format(time.RFC3339)}, key, &second)
	if status != http.StatusOK {
		t.Fatalf("replayed idempotent write status=%d", status)
	}
	if second.Operator != first.Operator || second.Session.CurrentSession.OpenedAt != first.Session.CurrentSession.OpenedAt {
		t.Fatalf("idempotent replay did not return first response")
	}
	var conflict map[string]any
	status = env.writeJSON(t, http.MethodPost, "/api/v1/city/open", map[string]any{"operator": "different", "openedAt": env.now.Format(time.RFC3339)}, key, &conflict)
	if status != http.StatusConflict {
		t.Fatalf("idempotency conflict status=%d body=%v", status, conflict)
	}

	req, err = http.NewRequest(http.MethodPost, env.server.URL+"/api/v1/city/close", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-NCFMS-CSRF", env.csrf)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing close idempotency key status=%d", resp.StatusCode)
	}

	var closeFirst AppState
	status = env.writeJSON(t, http.MethodPost, "/api/v1/city/close", map[string]any{}, "same-close-key", &closeFirst)
	if status != http.StatusOK {
		t.Fatalf("first close status=%d", status)
	}
	var closeSecond AppState
	status = env.writeJSON(t, http.MethodPost, "/api/v1/city/close", map[string]any{}, "same-close-key", &closeSecond)
	if status != http.StatusOK {
		t.Fatalf("replayed close status=%d", status)
	}
	if closeFirst.Session.CurrentSession != nil || closeSecond.Session.CurrentSession != nil {
		t.Fatalf("close did not leave session inactive: first=%#v second=%#v", closeFirst.Session.CurrentSession, closeSecond.Session.CurrentSession)
	}
}

func TestBackupOpenCityMigrationAndFailure(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 6, 18, 10, 0, 0, 0, loc)
	dataDir := t.TempDir()
	store, err := OpenStore(context.Background(), Config{DataDir: dataDir, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenCity(context.Background(), "backup-op", now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	backups, err := filepath.Glob(filepath.Join(dataDir, "backups", "ncfms-open-city-*.db"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("open city backup count=%d", len(backups))
	}

	migrationDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(migrationDir, "ncfms.db"), []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	migrated, err := OpenStore(context.Background(), Config{DataDir: migrationDir, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	_ = migrated.Close()
	migrationBackups, err := filepath.Glob(filepath.Join(migrationDir, "backups", "ncfms-migration-*.db"))
	if err != nil {
		t.Fatal(err)
	}
	if len(migrationBackups) != 1 {
		t.Fatalf("migration backup count=%d", len(migrationBackups))
	}

	failDir := t.TempDir()
	failStore, err := OpenStore(context.Background(), Config{DataDir: failDir, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(failDir, "backups"), []byte("not a directory"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := failStore.OpenCity(context.Background(), "blocked", now.Format(time.RFC3339)); err == nil {
		t.Fatalf("open city succeeded despite backup failure")
	}
	_ = failStore.Close()
}

func TestExportDTOAndXLSXSheets(t *testing.T) {
	env := newTestEnv(t)
	env.openCity(t, "exporter")
	state := env.enterPlayer(t, "enter-export", "01234", "导出居民", 1)
	role := findPlayer(t, state, "01234")
	status := env.writeJSON(t, http.MethodPost, "/api/v1/travel/extensions", map[string]any{"travelId": role.TravelID, "hours": -0.5}, "export-negative-adjust", &state)
	if status != http.StatusOK {
		t.Fatalf("negative export adjust status=%d", status)
	}
	env.gold(t, "gold-export", "01234", GoldAllocate, 2)
	exported, err := env.store.ExportData(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"数据库", "金条流水", "玩家进出城记录", "身份历史", "时长增加记录", "开城记录", "已取消进出城记录"}
	if len(exported.Sheets) != len(want) {
		t.Fatalf("sheet count=%d", len(exported.Sheets))
	}
	for i, name := range want {
		if exported.Sheets[i].Name != name {
			t.Fatalf("sheet[%d]=%q want %q", i, exported.Sheets[i].Name, name)
		}
	}
	dbRow := firstRow(t, exported, "数据库")
	if dbRow["编号"] != "01234" {
		t.Fatalf("export code lost leading zero: %#v", dbRow)
	}
	travelRow := firstRow(t, exported, "玩家进出城记录")
	if got := travelRow["时长增加记录"]; !strings.Contains(got, "-30分钟") {
		t.Fatalf("travel extension display = %q", got)
	}
	extensionSheet := sheetByName(exported, "时长增加记录")
	if got := extensionSheet.Columns[0]; got != "开城时间" {
		t.Fatalf("extension first column = %q", got)
	}
	extensionRow := firstRow(t, exported, "时长增加记录")
	if got := extensionRow["开城时间"]; got != "2026/6/18 15:00:00" {
		t.Fatalf("extension open city time = %q", got)
	}
	if got := extensionRow["增加分钟"]; got != "-30" {
		t.Fatalf("extension minutes = %q", got)
	}
	xlsx, err := XLSXBytes(exported)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(xlsx), int64(len(xlsx)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for i := range want {
		if !names["xl/worksheets/sheet"+itoa(int64(i+1))+".xml"] {
			t.Fatalf("xlsx missing worksheet %d", i+1)
		}
	}
}

func findNPC(t *testing.T, state AppState, code string) RoleDTO {
	t.Helper()
	for _, role := range state.Session.Roles {
		if role.Type == KindNPC && role.Code == code {
			return role
		}
	}
	t.Fatalf("npc %s not found in roles %#v", code, state.Session.Roles)
	return RoleDTO{}
}

func hasNPC(state AppState, code string) bool {
	for _, role := range state.Session.Roles {
		if role.Type == KindNPC && role.Code == code {
			return true
		}
	}
	return false
}

func findPlayer(t *testing.T, state AppState, code string) RoleDTO {
	t.Helper()
	for _, role := range state.Session.Roles {
		if role.Type == KindPlayer && role.Code == code {
			return role
		}
	}
	t.Fatalf("player %s not found in roles %#v", code, state.Session.Roles)
	return RoleDTO{}
}

func findRecordID(t *testing.T, state AppState, typ string) int64 {
	t.Helper()
	for _, record := range state.Session.Records {
		if record.TypeCode == typ {
			return record.ID
		}
	}
	t.Fatalf("record type %s not found in %#v", typ, state.Session.Records)
	return 0
}

func findRow(t *testing.T, data *ExportData, sheet, column, value string) map[string]string {
	t.Helper()
	for _, row := range sheetRows(t, data, sheet) {
		if row[column] == value {
			return row
		}
	}
	t.Fatalf("sheet %s has no row with %s=%s", sheet, column, value)
	return nil
}
func firstRow(t *testing.T, data *ExportData, sheet string) map[string]string {
	t.Helper()
	rows := sheetRows(t, data, sheet)
	if len(rows) == 0 {
		t.Fatalf("sheet %s has no rows", sheet)
	}
	return rows[0]
}

func sheetRows(t *testing.T, data *ExportData, sheet string) []map[string]string {
	t.Helper()
	for _, s := range data.Sheets {
		if s.Name == sheet {
			return s.Rows
		}
	}
	t.Fatalf("sheet %s not found", sheet)
	return nil
}

func assertColumnPresence(t *testing.T, store *Store, table, column string, want bool) {
	t.Helper()
	got, err := store.tableHasColumn(context.Background(), table, column)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("column %s.%s presence=%v want %v", table, column, got, want)
	}
}
func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}
