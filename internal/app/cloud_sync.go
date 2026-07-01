package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const ncptCloudSourceName = "nirvana-city-personnel-terminal"

type NCPTCloudSyncInput struct {
	AdminBaseURL string `json:"adminBaseUrl"`
	Password     string `json:"password"`
}

type NCPTSyncRequest struct {
	ClientUploadedAt string                   `json:"clientUploadedAt"`
	Source           map[string]any           `json:"source,omitempty"`
	Residents        []NCPTResidentInput      `json:"residents"`
	GoldRecords      []NCPTGoldInput          `json:"goldRecords"`
	TravelRecords    []NCPTTravelInput        `json:"travelRecords"`
	IdentityHistory  []NCPTIdentityHistory    `json:"identityHistory"`
	CitySessions     []NCPTCitySessionInput   `json:"citySessions"`
	TravelExtensions []NCPTTravelExtension    `json:"travelExtensions"`
	NPCPanelState    []NCPTNPCPanelStateInput `json:"npcPanelState"`
}

type NCPTResidentInput struct {
	Code            string `json:"code"`
	Name            string `json:"name"`
	Kind            string `json:"kind"`
	Balance         int    `json:"balance"`
	IdentityCurrent string `json:"identityCurrent"`
	Remark          string `json:"remark"`
	Visible         *bool  `json:"visible,omitempty"`
	UpdatedAt       string `json:"updatedAt"`
}

type NCPTGoldInput struct {
	ClientRecordID string `json:"clientRecordId"`
	ResidentCode   string `json:"residentCode"`
	ResidentName   string `json:"residentName"`
	Identity       string `json:"identity"`
	RecordType     string `json:"recordType"`
	Amount         int    `json:"amount"`
	BalanceAfter   int    `json:"balanceAfter"`
	Remark         string `json:"remark"`
	AffectBalance  *bool  `json:"affectBalance,omitempty"`
	Voided         bool   `json:"voided"`
	Operator       string `json:"operator"`
	OccurredAt     string `json:"occurredAt"`
}

type NCPTTravelInput struct {
	ClientRecordID   string `json:"clientRecordId"`
	SessionOpenedAt  string `json:"sessionOpenedAt"`
	ResidentCode     string `json:"residentCode"`
	ResidentName     string `json:"residentName"`
	Identity         string `json:"identity"`
	EnterAt          string `json:"enterAt"`
	LeaveAt          string `json:"leaveAt"`
	StayMinutes      int    `json:"stayMinutes"`
	CanceledAt       string `json:"canceledAt,omitempty"`
	HiddenAt         string `json:"hiddenAt,omitempty"`
	HiddenAfterLeave bool   `json:"hiddenAfterLeave,omitempty"`
	Operator         string `json:"operator"`
	CreatedAt        string `json:"createdAt"`
}

type NCPTIdentityHistory struct {
	ClientRecordID string `json:"clientRecordId"`
	ResidentCode   string `json:"residentCode"`
	ResidentName   string `json:"residentName"`
	Identity       string `json:"identity"`
	OccurredAt     string `json:"occurredAt"`
	DeletedAt      string `json:"deletedAt,omitempty"`
}

type NCPTCitySessionInput struct {
	OpenedAt string `json:"openedAt"`
	ClosedAt string `json:"closedAt,omitempty"`
	Operator string `json:"operator"`
	Note     string `json:"note"`
}

type NCPTTravelExtension struct {
	ClientRecordID  string `json:"clientRecordId"`
	TravelRecordID  string `json:"travelRecordId,omitempty"`
	SessionOpenedAt string `json:"sessionOpenedAt"`
	ResidentCode    string `json:"residentCode"`
	ResidentName    string `json:"residentName"`
	AddedMinutes    int    `json:"addedMinutes"`
	OccurredAt      string `json:"occurredAt"`
	Operator        string `json:"operator"`
}

type NCPTNPCPanelStateInput struct {
	ResidentCode string `json:"residentCode"`
	Visible      bool   `json:"visible"`
	UpdatedAt    string `json:"updatedAt"`
}

type NCPTSyncStats struct {
	Residents                    NCPTChangeStats `json:"residents"`
	GoldRecords                  NCPTChangeStats `json:"goldRecords"`
	TravelRecords                NCPTChangeStats `json:"travelRecords"`
	ArchivedPayload              bool            `json:"archivedPayload"`
	IgnoredCanceledTravelRecords int             `json:"ignoredCanceledTravelRecords"`
	BatchID                      string          `json:"batchId"`
}

type NCPTChangeStats struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
}

type ncptLoginResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expiresAt"`
}

type statusError struct {
	status  int
	message string
}

func (e statusError) Error() string {
	return e.message
}

func (e statusError) StatusCode() int {
	return e.status
}

func (s *Store) UploadNCPTSnapshot(ctx context.Context, in NCPTCloudSyncInput, client *http.Client) (NCPTSyncStats, error) {
	adminBaseURL, err := normalizeAdminBaseURL(in.AdminBaseURL)
	if err != nil {
		return NCPTSyncStats{}, err
	}
	password := strings.TrimSpace(in.Password)
	if password == "" {
		return NCPTSyncStats{}, fmt.Errorf("%w: upload password is required", ErrBadRequest)
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	payload, err := s.BuildNCPTSyncRequest(ctx)
	if err != nil {
		return NCPTSyncStats{}, err
	}
	login, err := postAdminJSON[ncptLoginResponse](ctx, client, adminBaseURL+"/api/ncpt/auth/login", "", map[string]string{"password": password}, "admin login")
	if err != nil {
		return NCPTSyncStats{}, err
	}
	if strings.TrimSpace(login.Token) == "" {
		return NCPTSyncStats{}, statusError{status: http.StatusBadGateway, message: "admin login failed: empty token"}
	}
	stats, err := postAdminJSON[NCPTSyncStats](ctx, client, adminBaseURL+"/api/ncpt/sync", login.Token, payload, "admin sync")
	if err != nil {
		return NCPTSyncStats{}, err
	}
	return stats, nil
}

func (s *Store) BuildNCPTSyncRequest(ctx context.Context) (NCPTSyncRequest, error) {
	request := NCPTSyncRequest{
		ClientUploadedAt: s.nowString(),
		Source: map[string]any{
			"name":          ncptCloudSourceName,
			"schemaVersion": schemaVersion,
		},
		Residents:        []NCPTResidentInput{},
		GoldRecords:      []NCPTGoldInput{},
		TravelRecords:    []NCPTTravelInput{},
		IdentityHistory:  []NCPTIdentityHistory{},
		CitySessions:     []NCPTCitySessionInput{},
		TravelExtensions: []NCPTTravelExtension{},
		NPCPanelState:    []NCPTNPCPanelStateInput{},
	}

	var err error
	if request.Residents, err = s.ncptResidents(ctx); err != nil {
		return request, err
	}
	if request.GoldRecords, err = s.ncptGoldRecords(ctx); err != nil {
		return request, err
	}
	if request.TravelRecords, err = s.ncptTravelRecords(ctx); err != nil {
		return request, err
	}
	if request.IdentityHistory, err = s.ncptIdentityHistory(ctx); err != nil {
		return request, err
	}
	if request.CitySessions, err = s.ncptCitySessions(ctx); err != nil {
		return request, err
	}
	if request.TravelExtensions, err = s.ncptTravelExtensions(ctx); err != nil {
		return request, err
	}
	if request.NPCPanelState, err = s.ncptNPCPanelState(ctx); err != nil {
		return request, err
	}
	return request, nil
}

func (s *Store) ncptResidents(ctx context.Context) ([]NCPTResidentInput, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT code, name, kind, balance, identity_current, remark, updated_at
		FROM residents ORDER BY kind, code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []NCPTResidentInput{}
	for rows.Next() {
		var code, name, kind, identity, remark, updatedAt string
		var balance int64
		if err := rows.Scan(&code, &name, &kind, &balance, &identity, &remark, &updatedAt); err != nil {
			return nil, err
		}
		balanceInt, err := int64ToInt(balance, "resident balance")
		if err != nil {
			return nil, err
		}
		updated, err := s.ncptTime(updatedAt, "resident updatedAt")
		if err != nil {
			return nil, err
		}
		record := NCPTResidentInput{
			Code:            normalizeCode(code),
			Name:            strings.TrimSpace(name),
			Kind:            kind,
			Balance:         balanceInt,
			IdentityCurrent: normalizeIdentity(identity),
			Remark:          strings.TrimSpace(remark),
			UpdatedAt:       updated,
		}
		if kind == KindNPC {
			visible := s.isNPCVisible(ctx, code)
			record.Visible = &visible
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func (s *Store) ncptGoldRecords(ctx context.Context) ([]NCPTGoldInput, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, resident_code, resident_name_snapshot, identity_snapshot, record_type,
		amount, balance_after, remark, affect_balance, voided, operator, occurred_at
		FROM gold_records ORDER BY occurred_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []NCPTGoldInput{}
	for rows.Next() {
		var id, amount, balance int64
		var affectBalance, voided int
		var code, name, identity, recordType, remark, operator, occurredAt string
		if err := rows.Scan(&id, &code, &name, &identity, &recordType, &amount, &balance, &remark, &affectBalance, &voided, &operator, &occurredAt); err != nil {
			return nil, err
		}
		amountInt, err := int64ToInt(amount, "gold amount")
		if err != nil {
			return nil, err
		}
		balanceInt, err := int64ToInt(balance, "gold balanceAfter")
		if err != nil {
			return nil, err
		}
		occurred, err := s.ncptTime(occurredAt, "gold occurredAt")
		if err != nil {
			return nil, err
		}
		affects := affectBalance != 0
		out = append(out, NCPTGoldInput{
			ClientRecordID: strconv.FormatInt(id, 10),
			ResidentCode:   normalizeCode(code),
			ResidentName:   strings.TrimSpace(name),
			Identity:       strings.TrimSpace(identity),
			RecordType:     strings.TrimSpace(recordType),
			Amount:         amountInt,
			BalanceAfter:   balanceInt,
			Remark:         strings.TrimSpace(remark),
			AffectBalance:  &affects,
			Voided:         voided != 0,
			Operator:       strings.TrimSpace(operator),
			OccurredAt:     occurred,
		})
	}
	return out, rows.Err()
}

func (s *Store) ncptTravelRecords(ctx context.Context) ([]NCPTTravelInput, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_opened_at, resident_code, resident_name_snapshot, identity_snapshot,
		enter_at, leave_at, stay_minutes, canceled_at, hidden_at, hidden_after_leave, operator, created_at
		FROM travel_records ORDER BY enter_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []NCPTTravelInput{}
	for rows.Next() {
		var id int64
		var sessionOpenedAt, code, name, identity, enterAt, leaveAt, operator, createdAt string
		var stayMinutes, hiddenAfterLeave int
		var canceledAt, hiddenAt sql.NullString
		if err := rows.Scan(&id, &sessionOpenedAt, &code, &name, &identity, &enterAt, &leaveAt, &stayMinutes, &canceledAt, &hiddenAt, &hiddenAfterLeave, &operator, &createdAt); err != nil {
			return nil, err
		}
		sessionOpenedAt, err = s.ncptTime(sessionOpenedAt, "travel sessionOpenedAt")
		if err != nil {
			return nil, err
		}
		enterAt, err = s.ncptTime(enterAt, "travel enterAt")
		if err != nil {
			return nil, err
		}
		leaveAt, err = s.ncptTime(leaveAt, "travel leaveAt")
		if err != nil {
			return nil, err
		}
		createdAt, err = s.ncptTime(createdAt, "travel createdAt")
		if err != nil {
			return nil, err
		}
		canceled, err := s.ncptNullTime(canceledAt, "travel canceledAt")
		if err != nil {
			return nil, err
		}
		hidden, err := s.ncptNullTime(hiddenAt, "travel hiddenAt")
		if err != nil {
			return nil, err
		}
		out = append(out, NCPTTravelInput{
			ClientRecordID:   strconv.FormatInt(id, 10),
			SessionOpenedAt:  sessionOpenedAt,
			ResidentCode:     normalizeCode(code),
			ResidentName:     strings.TrimSpace(name),
			Identity:         strings.TrimSpace(identity),
			EnterAt:          enterAt,
			LeaveAt:          leaveAt,
			StayMinutes:      stayMinutes,
			CanceledAt:       canceled,
			HiddenAt:         hidden,
			HiddenAfterLeave: hiddenAfterLeave != 0,
			Operator:         strings.TrimSpace(operator),
			CreatedAt:        createdAt,
		})
	}
	return out, rows.Err()
}

func (s *Store) ncptIdentityHistory(ctx context.Context) ([]NCPTIdentityHistory, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, resident_code, resident_name_snapshot, identity, occurred_at, deleted_at
		FROM identity_history ORDER BY occurred_at ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []NCPTIdentityHistory{}
	for rows.Next() {
		var id int64
		var code, name, identity, occurredAt string
		var deletedAt sql.NullString
		if err := rows.Scan(&id, &code, &name, &identity, &occurredAt, &deletedAt); err != nil {
			return nil, err
		}
		occurred, err := s.ncptTime(occurredAt, "identity occurredAt")
		if err != nil {
			return nil, err
		}
		deleted, err := s.ncptNullTime(deletedAt, "identity deletedAt")
		if err != nil {
			return nil, err
		}
		out = append(out, NCPTIdentityHistory{
			ClientRecordID: strconv.FormatInt(id, 10),
			ResidentCode:   normalizeCode(code),
			ResidentName:   strings.TrimSpace(name),
			Identity:       strings.TrimSpace(identity),
			OccurredAt:     occurred,
			DeletedAt:      deleted,
		})
	}
	return out, rows.Err()
}

func (s *Store) ncptCitySessions(ctx context.Context) ([]NCPTCitySessionInput, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT opened_at, closed_at, operator, note FROM city_sessions ORDER BY opened_at ASC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []NCPTCitySessionInput{}
	for rows.Next() {
		var openedAt, operator, note string
		var closedAt sql.NullString
		if err := rows.Scan(&openedAt, &closedAt, &operator, &note); err != nil {
			return nil, err
		}
		opened, err := s.ncptTime(openedAt, "city openedAt")
		if err != nil {
			return nil, err
		}
		closed, err := s.ncptNullTime(closedAt, "city closedAt")
		if err != nil {
			return nil, err
		}
		out = append(out, NCPTCitySessionInput{
			OpenedAt: opened,
			ClosedAt: closed,
			Operator: strings.TrimSpace(operator),
			Note:     strings.TrimSpace(note),
		})
	}
	return out, rows.Err()
}

func (s *Store) ncptTravelExtensions(ctx context.Context) ([]NCPTTravelExtension, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT e.id, e.travel_id, t.session_opened_at, t.resident_code, t.resident_name_snapshot,
		e.added_minutes, e.occurred_at, e.operator
		FROM travel_extensions e
		JOIN travel_records t ON t.id = e.travel_id
		ORDER BY e.occurred_at ASC, e.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []NCPTTravelExtension{}
	for rows.Next() {
		var id, travelID int64
		var sessionOpenedAt, code, name, occurredAt, operator string
		var addedMinutes int
		if err := rows.Scan(&id, &travelID, &sessionOpenedAt, &code, &name, &addedMinutes, &occurredAt, &operator); err != nil {
			return nil, err
		}
		sessionOpenedAt, err = s.ncptTime(sessionOpenedAt, "travel extension sessionOpenedAt")
		if err != nil {
			return nil, err
		}
		occurredAt, err = s.ncptTime(occurredAt, "travel extension occurredAt")
		if err != nil {
			return nil, err
		}
		out = append(out, NCPTTravelExtension{
			ClientRecordID:  strconv.FormatInt(id, 10),
			TravelRecordID:  strconv.FormatInt(travelID, 10),
			SessionOpenedAt: sessionOpenedAt,
			ResidentCode:    normalizeCode(code),
			ResidentName:    strings.TrimSpace(name),
			AddedMinutes:    addedMinutes,
			OccurredAt:      occurredAt,
			Operator:        strings.TrimSpace(operator),
		})
	}
	return out, rows.Err()
}

func (s *Store) ncptNPCPanelState(ctx context.Context) ([]NCPTNPCPanelStateInput, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT resident_code, visible, updated_at FROM npc_panel_state ORDER BY resident_code")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []NCPTNPCPanelStateInput{}
	for rows.Next() {
		var code, updatedAt string
		var visible int
		if err := rows.Scan(&code, &visible, &updatedAt); err != nil {
			return nil, err
		}
		updated, err := s.ncptTime(updatedAt, "npc panel updatedAt")
		if err != nil {
			return nil, err
		}
		out = append(out, NCPTNPCPanelStateInput{
			ResidentCode: normalizeCode(code),
			Visible:      visible != 0,
			UpdatedAt:    updated,
		})
	}
	return out, rows.Err()
}

func (s *Store) ncptTime(value, field string) (string, error) {
	parsed, err := parseDBTime(value, s.loc)
	if err != nil {
		return "", fmt.Errorf("%w: invalid %s: %v", ErrBadRequest, field, err)
	}
	return parsed.In(s.loc).Format(time.RFC3339), nil
}

func (s *Store) ncptNullTime(value sql.NullString, field string) (string, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return "", nil
	}
	return s.ncptTime(value.String, field)
}

func normalizeAdminBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: adminBaseUrl is required", ErrBadRequest)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("%w: adminBaseUrl must be an absolute http(s) URL", ErrBadRequest)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("%w: adminBaseUrl must use http or https", ErrBadRequest)
	}
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func postAdminJSON[T any](ctx context.Context, client *http.Client, endpoint, token string, body any, operation string) (T, error) {
	var zero T
	payload, err := json.Marshal(body)
	if err != nil {
		return zero, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return zero, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
	resp, err := client.Do(req)
	if err != nil {
		return zero, statusError{status: http.StatusBadGateway, message: operation + " failed: " + err.Error()}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return zero, statusError{status: http.StatusBadGateway, message: operation + " failed: " + err.Error()}
	}
	var envelope struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		message := strings.TrimSpace(string(data))
		if message == "" {
			message = resp.Status
		}
		return zero, statusError{status: http.StatusBadGateway, message: operation + " failed: " + message}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || envelope.Code != 0 {
		message := strings.TrimSpace(envelope.Message)
		if message == "" {
			message = resp.Status
		}
		return zero, statusError{status: http.StatusBadGateway, message: operation + " failed: " + message}
	}
	if len(envelope.Data) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Data), []byte("null")) {
		return zero, nil
	}
	var out T
	if err := json.Unmarshal(envelope.Data, &out); err != nil {
		return zero, statusError{status: http.StatusBadGateway, message: operation + " response decode failed: " + err.Error()}
	}
	return out, nil
}

func int64ToInt(value int64, field string) (int, error) {
	converted := int(value)
	if int64(converted) != value {
		return 0, fmt.Errorf("%w: %s is out of range", ErrBadRequest, field)
	}
	return converted, nil
}
