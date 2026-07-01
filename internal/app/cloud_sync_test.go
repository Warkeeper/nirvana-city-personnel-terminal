package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildNCPTSyncRequestMapsFullSnapshot(t *testing.T) {
	env := newTestEnv(t)
	env.openCity(t, "sync-op")
	state := env.enterPlayer(t, "sync-enter", "01234", "零号居民", 7)
	travelID := findPlayer(t, state, "01234").TravelID

	status := env.writeJSON(t, http.MethodPost, "/api/v1/identity", map[string]any{"code": "01234", "identity": "保安部正式员工"}, "sync-identity", &state)
	if status != http.StatusOK {
		t.Fatalf("set identity status=%d", status)
	}
	var identityID int64
	if err := env.store.db.QueryRow("SELECT id FROM identity_history WHERE resident_code = ? ORDER BY id DESC LIMIT 1", "01234").Scan(&identityID); err != nil {
		t.Fatal(err)
	}
	status = env.writeJSON(t, http.MethodDelete, "/api/v1/identity/history/"+itoa(identityID), map[string]any{}, "sync-identity-delete", &state)
	if status != http.StatusOK {
		t.Fatalf("delete identity history status=%d", status)
	}

	env.now = env.now.Add(10 * 60 * 1000 * 1000 * 1000)
	status = env.writeJSON(t, http.MethodPost, "/api/v1/travel/extensions", map[string]any{"travelId": travelID, "hours": 0.5}, "sync-extension", &state)
	if status != http.StatusOK {
		t.Fatalf("extend travel status=%d", status)
	}
	env.now = env.now.Add(10 * 60 * 1000 * 1000 * 1000)
	status = env.writeJSON(t, http.MethodPost, "/api/v1/travel/hide", map[string]any{"travelId": travelID}, "sync-hide", &state)
	if status != http.StatusOK {
		t.Fatalf("hide travel status=%d", status)
	}
	state = env.gold(t, "sync-gold", "01234", GoldIn, 4)

	status = env.writeJSON(t, http.MethodPost, "/api/v1/residents/npc", map[string]any{
		"code": "09999", "name": "常驻同步", "identity": "物资部正式员工", "balance": 3, "visible": true,
	}, "sync-npc", &state)
	if status != http.StatusOK {
		t.Fatalf("npc add status=%d", status)
	}
	status = env.writeJSON(t, http.MethodPost, "/api/v1/npc/panel", map[string]any{"code": "09999", "visible": false}, "sync-npc-hide", &state)
	if status != http.StatusOK {
		t.Fatalf("npc hide status=%d", status)
	}

	payload, err := env.store.BuildNCPTSyncRequest(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if payload.Source["name"] != ncptCloudSourceName {
		t.Fatalf("source = %#v", payload.Source)
	}
	player := findNCPTResident(t, payload.Residents, "01234")
	if player.Code != "01234" || player.Balance != 11 || player.Kind != KindPlayer || player.IdentityCurrent != "保安部正式员工" {
		t.Fatalf("player snapshot = %#v", player)
	}
	npc := findNCPTResident(t, payload.Residents, "09999")
	if npc.Visible == nil || *npc.Visible {
		t.Fatalf("npc visible should be false: %#v", npc)
	}
	if len(payload.GoldRecords) != 1 || payload.GoldRecords[0].ResidentCode != "01234" || payload.GoldRecords[0].Amount != 4 || payload.GoldRecords[0].AffectBalance == nil || !*payload.GoldRecords[0].AffectBalance {
		t.Fatalf("gold records = %#v", payload.GoldRecords)
	}
	if len(payload.TravelRecords) != 1 || payload.TravelRecords[0].ClientRecordID != itoa(travelID) || payload.TravelRecords[0].CanceledAt == "" || payload.TravelRecords[0].HiddenAt == "" || payload.TravelRecords[0].HiddenAfterLeave {
		t.Fatalf("travel records = %#v", payload.TravelRecords)
	}
	deletedIdentity := findNCPTIdentityHistory(t, payload.IdentityHistory, itoa(identityID))
	if deletedIdentity.DeletedAt == "" {
		t.Fatalf("identity history = %#v", payload.IdentityHistory)
	}
	if len(payload.CitySessions) != 1 || payload.CitySessions[0].OpenedAt == "" {
		t.Fatalf("city sessions = %#v", payload.CitySessions)
	}
	if len(payload.TravelExtensions) != 1 || payload.TravelExtensions[0].TravelRecordID != itoa(travelID) || payload.TravelExtensions[0].AddedMinutes != 30 {
		t.Fatalf("travel extensions = %#v", payload.TravelExtensions)
	}
	if len(payload.NPCPanelState) != 1 || payload.NPCPanelState[0].ResidentCode != "09999" || payload.NPCPanelState[0].Visible {
		t.Fatalf("npc panel state = %#v", payload.NPCPanelState)
	}
}

func TestNCPTCloudSyncEndpointLogsInAndUploads(t *testing.T) {
	env := newTestEnv(t)
	env.openCity(t, "sync-op")
	env.enterPlayer(t, "sync-http-enter", "01234", "同步居民", 1)

	var received NCPTSyncRequest
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ncpt/auth/login":
			if r.Method != http.MethodPost {
				t.Fatalf("login method = %s", r.Method)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["password"] != "secret" {
				t.Fatalf("login password = %#v", body)
			}
			writeAdminEnvelope(w, http.StatusOK, 0, "ok", map[string]any{"token": "token-1", "expiresAt": int64(1)})
		case "/api/ncpt/sync":
			if r.Header.Get("Authorization") != "Bearer token-1" {
				t.Fatalf("sync authorization = %q", r.Header.Get("Authorization"))
			}
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Fatal(err)
			}
			writeAdminEnvelope(w, http.StatusOK, 0, "ok", NCPTSyncStats{
				Residents:       NCPTChangeStats{Created: 1},
				ArchivedPayload: true,
				BatchID:         "ncpt_batch_test",
			})
		default:
			t.Fatalf("unexpected admin path %s", r.URL.Path)
		}
	}))
	defer admin.Close()

	var result NCPTSyncStats
	status := env.writeJSON(t, http.MethodPost, "/api/v1/cloud/ncpt/sync", map[string]any{
		"adminBaseUrl": admin.URL + "/admin",
		"password":     "secret",
	}, "cloud-sync-ok", &result)
	if status != http.StatusOK {
		t.Fatalf("cloud sync status=%d result=%#v", status, result)
	}
	if result.BatchID != "ncpt_batch_test" || result.Residents.Created != 1 || !result.ArchivedPayload {
		t.Fatalf("result = %#v", result)
	}
	if len(received.Residents) != 1 || received.Residents[0].Code != "01234" {
		t.Fatalf("received payload = %#v", received)
	}
}

func TestNCPTCloudSyncEndpointReturnsAdminError(t *testing.T) {
	env := newTestEnv(t)
	env.openCity(t, "sync-op")
	env.enterPlayer(t, "sync-http-error-enter", "01234", "同步居民", 1)

	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAdminEnvelope(w, http.StatusUnauthorized, 401, "ncpt 上传密码错误", nil)
	}))
	defer admin.Close()

	var body map[string]any
	status := env.writeJSON(t, http.MethodPost, "/api/v1/cloud/ncpt/sync", map[string]any{
		"adminBaseUrl": admin.URL,
		"password":     "bad",
	}, "cloud-sync-error", &body)
	if status != http.StatusBadGateway {
		t.Fatalf("cloud sync error status=%d body=%#v", status, body)
	}
	if !strings.Contains(body["error"].(string), "ncpt 上传密码错误") {
		t.Fatalf("error body = %#v", body)
	}
}

func findNCPTResident(t *testing.T, residents []NCPTResidentInput, code string) NCPTResidentInput {
	t.Helper()
	for _, resident := range residents {
		if resident.Code == code {
			return resident
		}
	}
	t.Fatalf("resident %s not found in %#v", code, residents)
	return NCPTResidentInput{}
}

func findNCPTIdentityHistory(t *testing.T, items []NCPTIdentityHistory, id string) NCPTIdentityHistory {
	t.Helper()
	for _, item := range items {
		if item.ClientRecordID == id {
			return item
		}
	}
	t.Fatalf("identity history %s not found in %#v", id, items)
	return NCPTIdentityHistory{}
}

func writeAdminEnvelope(w http.ResponseWriter, status, code int, message string, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":    code,
		"message": message,
		"data":    data,
	})
}
