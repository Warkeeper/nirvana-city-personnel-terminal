package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var (
	ErrNotFound   = errors.New("not found")
	ErrConflict   = errors.New("conflict")
	ErrBadRequest = errors.New("bad request")
)

func (s *Store) OpenCity(ctx context.Context, operator, openedAt string) (*AppState, error) {
	operator = strings.TrimSpace(operator)
	if operator == "" {
		return nil, fmt.Errorf("%w: operator is required", ErrBadRequest)
	}
	openedAtTime, err := parseOpenCityTime(openedAt, s.loc)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadRequest, err)
	}
	openedAtText := openedAtTime.In(s.loc).Format(time.RFC3339)
	if _, err := s.Backup(ctx, "open-city"); err != nil {
		return nil, fmt.Errorf("open city backup failed: %w", err)
	}
	if err := s.withTx(ctx, func(tx *sql.Tx) error {
		var existing string
		err := tx.QueryRowContext(ctx, "SELECT opened_at FROM city_sessions WHERE opened_at = ?", openedAtText).Scan(&existing)
		if err == nil {
			return fmt.Errorf("%w: open city time already exists", ErrConflict)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		var active CitySession
		var closed sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT opened_at, closed_at, operator FROM city_sessions
			WHERE closed_at IS NULL ORDER BY opened_at DESC LIMIT 1`).Scan(&active.OpenedAt, &closed, &active.Operator)
		if err == nil {
			activeOpenedAt, err := parseDBTime(active.OpenedAt, s.loc)
			if err != nil {
				return err
			}
			if !openedAtTime.After(activeOpenedAt) {
				return fmt.Errorf("%w: open city time must be later than the active city session", ErrBadRequest)
			}
			if _, err := tx.ExecContext(ctx, "UPDATE city_sessions SET closed_at = ? WHERE opened_at = ? AND closed_at IS NULL", openedAtText, active.OpenedAt); err != nil {
				return err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}

		if _, err := tx.ExecContext(ctx, "INSERT INTO city_sessions(opened_at, operator) VALUES(?, ?)", openedAtText, operator); err != nil {
			return err
		}
		return s.insertAudit(ctx, tx, "city.open", "开城："+operator, operator, openedAtText)
	}); err != nil {
		return nil, err
	}
	return s.State(ctx, "")
}

func (s *Store) CloseCity(ctx context.Context) (*AppState, error) {
	now := s.nowString()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		session, err := s.currentSessionTx(ctx, tx)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE city_sessions SET closed_at = ? WHERE opened_at = ? AND closed_at IS NULL", now, session.OpenedAt); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE travel_records
			SET hidden_at = ?, hidden_after_leave = 1
			WHERE session_opened_at = ? AND canceled_at IS NULL AND hidden_at IS NULL`, now, session.OpenedAt); err != nil {
			return err
		}
		return s.insertAudit(ctx, tx, "city.close", "闭城："+session.Operator, session.Operator, now)
	})
	if err != nil {
		return nil, err
	}
	return s.State(ctx, "")
}

type EnterResidentInput struct {
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Balance     int64   `json:"balance"`
	Identity    string  `json:"identity"`
	Remark      string  `json:"remark"`
	EnterTime   string  `json:"enterTime"`
	StayHours   float64 `json:"stayHours"`
	StayMinutes int     `json:"stayMinutes"`
}

func (s *Store) EnterPlayer(ctx context.Context, in EnterResidentInput) (*AppState, error) {
	in.Code = normalizeCode(in.Code)
	in.Name = strings.TrimSpace(in.Name)
	in.Identity = normalizeIdentity(in.Identity)
	in.Remark = strings.TrimSpace(in.Remark)
	if in.Code == "" || in.Name == "" {
		return nil, fmt.Errorf("%w: code and name are required", ErrBadRequest)
	}
	stayMinutes := in.StayMinutes
	if stayMinutes <= 0 {
		if in.StayHours <= 0 {
			return nil, fmt.Errorf("%w: stay time is required", ErrBadRequest)
		}
		stayMinutes = int(math.Round(in.StayHours * 60))
	}
	if stayMinutes <= 0 {
		return nil, fmt.Errorf("%w: stay time is invalid", ErrBadRequest)
	}
	now := s.nowString()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		session, err := s.currentSessionTx(ctx, tx)
		if err != nil {
			return err
		}
		sessionOpenedAt, err := parseDBTime(session.OpenedAt, s.loc)
		if err != nil {
			return err
		}
		enterAt, err := parseEnterTime(in.EnterTime, sessionOpenedAt, s.loc)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrBadRequest, err)
		}
		leaveAt := enterAt.Add(time.Duration(stayMinutes) * time.Minute)
		var activeTravelID int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM travel_records
			WHERE session_opened_at = ? AND resident_code = ? AND canceled_at IS NULL AND hidden_at IS NULL
			LIMIT 1`, session.OpenedAt, in.Code).Scan(&activeTravelID)
		if err == nil {
			return fmt.Errorf("%w: resident is already in current city session", ErrConflict)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := s.ensureResidentTx(ctx, tx, in.Code, in.Name, KindPlayer, in.Balance, in.Identity, in.Remark, now); err != nil {
			return err
		}
		if err := s.setIdentityIfChangedTx(ctx, tx, in.Code, in.Identity, session.Operator, now); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO travel_records(
			session_opened_at, resident_code, resident_name_snapshot, identity_snapshot,
			enter_at, leave_at, stay_minutes, operator, created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			session.OpenedAt, in.Code, in.Name, in.Identity, enterAt.In(s.loc).Format(time.RFC3339),
			leaveAt.In(s.loc).Format(time.RFC3339), stayMinutes, session.Operator, now)
		if err != nil {
			if isActiveTravelUniqueConstraintError(err) {
				return fmt.Errorf("%w: resident is already in current city session", ErrConflict)
			}
			return err
		}
		return s.insertAudit(ctx, tx, "travel.enter", fmt.Sprintf("城邦居民进城：%s(%s)", in.Name, in.Code), session.Operator, now)
	})
	if err != nil {
		return nil, err
	}
	return s.State(ctx, "")
}

func isActiveTravelUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "idx_travel_records_active_unique") ||
		(strings.Contains(message, "unique constraint") &&
			strings.Contains(message, "travel_records.session_opened_at") &&
			strings.Contains(message, "travel_records.resident_code"))
}

type NPCInput struct {
	Code     string `json:"code"`
	Name     string `json:"name"`
	Balance  int64  `json:"balance"`
	Identity string `json:"identity"`
	Remark   string `json:"remark"`
	Visible  *bool  `json:"visible"`
}

func (s *Store) UpsertNPC(ctx context.Context, in NPCInput) (*AppState, error) {
	in.Code = normalizeCode(in.Code)
	in.Name = strings.TrimSpace(in.Name)
	in.Identity = normalizeIdentity(in.Identity)
	in.Remark = strings.TrimSpace(in.Remark)
	if in.Code == "" || in.Name == "" {
		return nil, fmt.Errorf("%w: code and name are required", ErrBadRequest)
	}
	visible := true
	if in.Visible != nil {
		visible = *in.Visible
	}
	now := s.nowString()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		session, _ := s.currentSessionTx(ctx, tx)
		operator := ""
		if session != nil {
			operator = session.Operator
		}
		var existing string
		err := tx.QueryRowContext(ctx, "SELECT code FROM residents WHERE code = ?", in.Code).Scan(&existing)
		if err == nil {
			return fmt.Errorf("%w: 居民编号已被占用", ErrConflict)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := s.ensureResidentTx(ctx, tx, in.Code, in.Name, KindNPC, in.Balance, in.Identity, in.Remark, now); err != nil {
			return err
		}
		if err := s.setIdentityIfChangedTx(ctx, tx, in.Code, in.Identity, operator, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO npc_panel_state(resident_code, visible, updated_at)
			VALUES(?, ?, ?)
			ON CONFLICT(resident_code) DO UPDATE SET visible = excluded.visible, updated_at = excluded.updated_at`,
			in.Code, boolInt(visible), now); err != nil {
			return err
		}
		return s.insertAudit(ctx, tx, "npc.upsert", fmt.Sprintf("常驻居民显示：%s(%s)", in.Name, in.Code), operator, now)
	})
	if err != nil {
		return nil, err
	}
	return s.State(ctx, "")
}

func (s *Store) UpdateResident(ctx context.Context, oldCode, newCode, name, remark string) (*AppState, error) {
	oldCode = normalizeCode(oldCode)
	newCode = normalizeCode(newCode)
	if newCode == "" {
		newCode = oldCode
	}
	name = strings.TrimSpace(name)
	remark = strings.TrimSpace(remark)
	if oldCode == "" || newCode == "" || name == "" {
		return nil, fmt.Errorf("%w: code and name are required", ErrBadRequest)
	}
	now := s.nowString()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var currentName, kind, identity, createdAt string
		var balance int64
		err := tx.QueryRowContext(ctx, `SELECT name, kind, balance, identity_current, created_at
			FROM residents WHERE code = ?`, oldCode).Scan(&currentName, &kind, &balance, &identity, &createdAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		notes := []string{}
		if currentName != name {
			notes = append(notes, "原姓名："+currentName)
		}
		if oldCode != newCode {
			notes = append(notes, "原编号："+oldCode)
		}
		finalRemark := appendProfileChangeNotes(remark, notes)
		operator := ""
		if session, _ := s.currentSessionTx(ctx, tx); session != nil {
			operator = session.Operator
		}
		if oldCode == newCode {
			res, err := tx.ExecContext(ctx, "UPDATE residents SET name = ?, remark = ?, updated_at = ? WHERE code = ?", name, finalRemark, now, oldCode)
			if err != nil {
				return err
			}
			affected, _ := res.RowsAffected()
			if affected == 0 {
				return ErrNotFound
			}
			return s.insertAudit(ctx, tx, "resident.update", fmt.Sprintf("居民资料更新：%s(%s)", name, oldCode), operator, now)
		}
		var existing string
		err = tx.QueryRowContext(ctx, "SELECT code FROM residents WHERE code = ?", newCode).Scan(&existing)
		if err == nil {
			return fmt.Errorf("%w: resident code already exists", ErrConflict)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO residents(code, name, kind, balance, identity_current, remark, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, newCode, name, kind, balance, identity, finalRemark, createdAt, now); err != nil {
			return err
		}
		for _, stmt := range []string{
			"UPDATE identity_history SET resident_code = ? WHERE resident_code = ?",
			"UPDATE gold_records SET resident_code = ? WHERE resident_code = ?",
			"UPDATE travel_records SET resident_code = ? WHERE resident_code = ?",
			"UPDATE npc_panel_state SET resident_code = ? WHERE resident_code = ?",
		} {
			if _, err := tx.ExecContext(ctx, stmt, newCode, oldCode); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM residents WHERE code = ?", oldCode); err != nil {
			return err
		}
		return s.insertAudit(ctx, tx, "resident.update", fmt.Sprintf("居民资料更新：%s(%s -> %s)", name, oldCode, newCode), operator, now)
	})
	if err != nil {
		return nil, err
	}
	return s.State(ctx, "")
}

func (s *Store) SetIdentity(ctx context.Context, code, identity string) (*AppState, error) {
	code = normalizeCode(code)
	identity = normalizeIdentity(identity)
	if code == "" {
		return nil, fmt.Errorf("%w: code is required", ErrBadRequest)
	}
	now := s.nowString()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		session, _ := s.currentSessionTx(ctx, tx)
		operator := ""
		if session != nil {
			operator = session.Operator
		}
		return s.setIdentityIfChangedTx(ctx, tx, code, identity, operator, now)
	})
	if err != nil {
		return nil, err
	}
	return s.State(ctx, "")
}

func (s *Store) DeleteIdentityHistory(ctx context.Context, id int64) (*AppState, error) {
	if id <= 0 {
		return nil, fmt.Errorf("%w: identity history id is required", ErrBadRequest)
	}
	now := s.nowString()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE identity_history SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL", now, id)
		if err != nil {
			return err
		}
		affected, _ := res.RowsAffected()
		if affected == 0 {
			return ErrNotFound
		}
		session, _ := s.currentSessionTx(ctx, tx)
		operator := ""
		if session != nil {
			operator = session.Operator
		}
		return s.insertAudit(ctx, tx, "identity.delete", fmt.Sprintf("删除身份历史：%d", id), operator, now)
	})
	if err != nil {
		return nil, err
	}
	return s.State(ctx, "")
}

type GoldInput struct {
	Code             string `json:"code"`
	Type             string `json:"type"`
	Amount           int64  `json:"amount"`
	Remark           string `json:"remark"`
	AllocateCategory string `json:"allocateCategory"`
	AllocateReason   string `json:"allocateReason"`
}

func (s *Store) CreateGoldRecord(ctx context.Context, in GoldInput) (*AppState, error) {
	in.Code = normalizeCode(in.Code)
	in.Type = strings.TrimSpace(in.Type)
	in.Remark = strings.TrimSpace(in.Remark)
	in.AllocateCategory = strings.TrimSpace(in.AllocateCategory)
	in.AllocateReason = strings.TrimSpace(in.AllocateReason)
	if in.Code == "" || in.Amount <= 0 {
		return nil, fmt.Errorf("%w: code and positive amount are required", ErrBadRequest)
	}
	if !validGoldType(in.Type) {
		return nil, fmt.Errorf("%w: invalid gold record type", ErrBadRequest)
	}
	now := s.nowString()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		operator := ""
		if session, _ := s.currentSessionTx(ctx, tx); session != nil {
			operator = session.Operator
		}
		resident, err := s.residentTx(ctx, tx, in.Code)
		if err != nil {
			return err
		}
		amount := signedGoldAmount(in.Type, in.Amount)
		affectBalance := in.Type != GoldAllocate
		nextBalance := resident.Balance
		if affectBalance {
			nextBalance += amount
			if _, err := tx.ExecContext(ctx, "UPDATE residents SET balance = ?, updated_at = ? WHERE code = ?", nextBalance, now, in.Code); err != nil {
				return err
			}
		}
		remark := in.Remark
		if in.Type == GoldAllocate {
			reason := in.AllocateCategory
			if reason == "" || reason == "自定义" {
				reason = in.AllocateReason
			}
			if reason == "" {
				reason = "其他"
			}
			if remark != "" {
				remark = "拨付原因：" + reason + "；" + remark
			} else {
				remark = "拨付原因：" + reason
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO gold_records(
			resident_code, resident_name_snapshot, identity_snapshot, record_type, amount,
			balance_after, remark, affect_balance, voided, balance_reverted, operator, occurred_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?)`,
			in.Code, resident.Name, resident.Identity, in.Type, amount, nextBalance, remark, boolInt(affectBalance), operator, now); err != nil {
			return err
		}
		return s.insertAudit(ctx, tx, "gold.create", fmt.Sprintf("%s：%s %d", goldTypeLabel(in.Type), resident.Name, amount), operator, now)
	})
	if err != nil {
		return nil, err
	}
	return s.State(ctx, "")
}

func (s *Store) VoidGoldRecord(ctx context.Context, id int64) (*AppState, error) {
	if id <= 0 {
		return nil, fmt.Errorf("%w: record id is required", ErrBadRequest)
	}
	now := s.nowString()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		session, err := s.currentSessionTx(ctx, tx)
		if err != nil {
			return err
		}
		var code, name, recordType string
		var amount int64
		var affectBalance, voided, balanceReverted int
		err = tx.QueryRowContext(ctx, `SELECT resident_code, resident_name_snapshot, record_type, amount, affect_balance, voided, balance_reverted
			FROM gold_records WHERE id = ?`, id).Scan(&code, &name, &recordType, &amount, &affectBalance, &voided, &balanceReverted)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if voided == 0 {
			if _, err := tx.ExecContext(ctx, "UPDATE gold_records SET voided = 1 WHERE id = ?", id); err != nil {
				return err
			}
		}
		if affectBalance != 0 && balanceReverted == 0 {
			if _, err := tx.ExecContext(ctx, "UPDATE residents SET balance = balance - ?, updated_at = ? WHERE code = ?", amount, now, code); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE gold_records SET balance_reverted = 1 WHERE id = ?", id); err != nil {
				return err
			}
		}
		return s.insertAudit(ctx, tx, "gold.void", fmt.Sprintf("作废流水：%s %s %d", name, goldTypeLabel(recordType), amount), session.Operator, now)
	})
	if err != nil {
		return nil, err
	}
	return s.State(ctx, "")
}

const minimumTravelStayMinutes = 30

func (s *Store) ExtendTravel(ctx context.Context, travelID int64, adjustHours float64) (*AppState, error) {
	if travelID <= 0 {
		return nil, fmt.Errorf("%w: travel id is required", ErrBadRequest)
	}
	if math.IsNaN(adjustHours) || math.IsInf(adjustHours, 0) || adjustHours == 0 {
		return nil, fmt.Errorf("%w: non-zero hours are required", ErrBadRequest)
	}
	roundedMinutes := math.Round(adjustHours * 60)
	if math.IsNaN(roundedMinutes) || math.IsInf(roundedMinutes, 0) ||
		roundedMinutes == 0 || roundedMinutes > float64(math.MaxInt32) || roundedMinutes < -float64(math.MaxInt32)-1 {
		return nil, fmt.Errorf("%w: adjusted minutes are invalid", ErrBadRequest)
	}
	addedMinutes := int(roundedMinutes)
	now := s.nowString()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		session, err := s.currentSessionTx(ctx, tx)
		if err != nil {
			return err
		}
		var enterRaw string
		var stay int
		err = tx.QueryRowContext(ctx, "SELECT enter_at, stay_minutes FROM travel_records WHERE id = ? AND canceled_at IS NULL", travelID).Scan(&enterRaw, &stay)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		enterAt, err := parseDBTime(enterRaw, s.loc)
		if err != nil {
			return err
		}
		nextStay := stay + addedMinutes
		if nextStay < minimumTravelStayMinutes {
			return fmt.Errorf("%w: stay time cannot be shorter than 0.5 hours", ErrBadRequest)
		}
		nextLeave := enterAt.Add(time.Duration(nextStay) * time.Minute).In(s.loc).Format(time.RFC3339)
		if _, err := tx.ExecContext(ctx, "UPDATE travel_records SET stay_minutes = ?, leave_at = ? WHERE id = ?", nextStay, nextLeave, travelID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO travel_extensions(travel_id, added_minutes, occurred_at, operator) VALUES(?, ?, ?, ?)", travelID, addedMinutes, now, session.Operator); err != nil {
			return err
		}
		return s.insertAudit(ctx, tx, "travel.extend", fmt.Sprintf("调整时长：%d %s", travelID, signedMinutesText(addedMinutes)), session.Operator, now)
	})
	if err != nil {
		return nil, err
	}
	return s.State(ctx, "")
}

func signedMinutesText(minutes int) string {
	if minutes > 0 {
		return fmt.Sprintf("+%d分钟", minutes)
	}
	return fmt.Sprintf("%d分钟", minutes)
}

func (s *Store) HideTravel(ctx context.Context, travelID int64) (*AppState, error) {
	if travelID <= 0 {
		return nil, fmt.Errorf("%w: travel id is required", ErrBadRequest)
	}
	nowTime := s.now().In(s.loc)
	now := nowTime.Format(time.RFC3339)
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		session, err := s.currentSessionTx(ctx, tx)
		if err != nil {
			return err
		}
		var leaveRaw, name, code string
		err = tx.QueryRowContext(ctx, "SELECT leave_at, resident_name_snapshot, resident_code FROM travel_records WHERE id = ?", travelID).Scan(&leaveRaw, &name, &code)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		leaveAt, err := parseDBTime(leaveRaw, s.loc)
		if err != nil {
			return err
		}
		if nowTime.Before(leaveAt) {
			if _, err := tx.ExecContext(ctx, "UPDATE travel_records SET canceled_at = ?, hidden_at = ?, hidden_after_leave = 0 WHERE id = ? AND canceled_at IS NULL", now, now, travelID); err != nil {
				return err
			}
			return s.insertAudit(ctx, tx, "travel.cancel", fmt.Sprintf("提前隐藏并取消进出城：%s(%s)", name, code), session.Operator, now)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE travel_records SET hidden_at = ?, hidden_after_leave = 1 WHERE id = ? AND hidden_at IS NULL", now, travelID); err != nil {
			return err
		}
		return s.insertAudit(ctx, tx, "travel.hide", fmt.Sprintf("离城后隐藏：%s(%s)", name, code), session.Operator, now)
	})
	if err != nil {
		return nil, err
	}
	return s.State(ctx, "")
}

func (s *Store) SetNPCVisible(ctx context.Context, code string, visible bool) (*AppState, error) {
	code = normalizeCode(code)
	if code == "" {
		return nil, fmt.Errorf("%w: code is required", ErrBadRequest)
	}
	now := s.nowString()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var kind string
		err := tx.QueryRowContext(ctx, "SELECT kind FROM residents WHERE code = ?", code).Scan(&kind)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if kind != KindNPC {
			return fmt.Errorf("%w: resident is not npc", ErrBadRequest)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO npc_panel_state(resident_code, visible, updated_at)
			VALUES(?, ?, ?)
			ON CONFLICT(resident_code) DO UPDATE SET visible = excluded.visible, updated_at = excluded.updated_at`,
			code, boolInt(visible), now); err != nil {
			return err
		}
		session, _ := s.currentSessionTx(ctx, tx)
		operator := ""
		if session != nil {
			operator = session.Operator
		}
		return s.insertAudit(ctx, tx, "npc.visible", fmt.Sprintf("常驻居民显示状态：%s=%v", code, visible), operator, now)
	})
	if err != nil {
		return nil, err
	}
	return s.State(ctx, "")
}

func (s *Store) State(ctx context.Context, csrfToken string) (*AppState, error) {
	state := &AppState{
		CSRFToken:          csrfToken,
		DefaultVisibleNPCs: append([]string(nil), DefaultVisibleNPCCodes...),
		ServerTime:         s.nowString(),
		SchemaVersion:      s.SchemaVersion(ctx),
	}
	session, err := s.currentSession(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var currentSession *CitySession
	if err == nil {
		currentSession = session
		state.Operator = session.Operator
	}
	dto := SessionDTO{
		Roles:                         []RoleDTO{},
		Records:                       []GoldRecordDTO{},
		SessionStartedAt:              "",
		Theme:                         "light",
		HiddenResidentCodes:           []string{},
		HiddenNPCKeys:                 []string{},
		SuppressedTravelResidentCodes: []string{},
		HistoricalNPCs:                []RoleDTO{},
		VisibleNPCCodes:               []string{},
		CurrentSession:                currentSession,
	}
	if currentSession != nil {
		dto.SessionStartedAt = currentSession.OpenedAt
	}
	npcs, visibleCodes, hiddenKeys, err := s.loadVisibleNPCRoles(ctx)
	if err != nil {
		return nil, err
	}
	dto.Roles = append(dto.Roles, npcs...)
	dto.VisibleNPCCodes = visibleCodes
	dto.HiddenNPCKeys = hiddenKeys
	if currentSession != nil {
		players, hidden, suppressed, err := s.loadTravelRoles(ctx, currentSession.OpenedAt)
		if err != nil {
			return nil, err
		}
		dto.Roles = append(dto.Roles, players...)
		dto.HiddenResidentCodes = hidden
		dto.SuppressedTravelResidentCodes = suppressed
	}
	stats, err := s.todayStats(ctx)
	if err != nil {
		return nil, err
	}
	state.Stats = stats
	records, err := s.loadCurrentSessionGoldRecords(ctx, currentSession)
	if err != nil {
		return nil, err
	}
	dto.Records = records
	latest, err := s.latestAudit(ctx)
	if err != nil {
		return nil, err
	}
	dto.LatestOperation = latest
	state.Session = dto
	return state, nil
}

type ResidentSearchInput struct {
	Query string
	Code  string
	Name  string
	Kind  string
	Limit int
}

func (s *Store) SearchResidents(ctx context.Context, in ResidentSearchInput) ([]RoleDTO, error) {
	query := strings.TrimSpace(in.Query)
	kind := strings.TrimSpace(in.Kind)
	code := normalizeCode(in.Code)
	name := strings.TrimSpace(in.Name)
	codeLike := ""
	matchAny := false
	if code == "" && name == "" && query != "" {
		if kind == KindPlayer {
			codeLike = normalizeCode(query)
		} else {
			codeLike = normalizeCode(query)
			name = query
			matchAny = true
		}
	}
	if in.Limit <= 0 || in.Limit > 50 {
		in.Limit = 10
	}
	if kind != "" && kind != KindPlayer && kind != KindNPC {
		return nil, fmt.Errorf("%w: invalid resident kind", ErrBadRequest)
	}
	roles, err := s.searchResidentRows(ctx, residentSearchOptions{
		Kind:      kind,
		CodeExact: code,
		CodeLike:  codeLike,
		NameLike:  name,
		Limit:     in.Limit,
		MatchAny:  matchAny,
	})
	if err != nil {
		return nil, err
	}
	return roles, nil
}

func (s *Store) Summary(ctx context.Context, code string) (map[string]any, error) {
	code = normalizeCode(code)
	if code == "" {
		return nil, fmt.Errorf("%w: code is required", ErrBadRequest)
	}
	var balance int64
	err := s.db.QueryRowContext(ctx, "SELECT balance FROM residents WHERE code = ?", code).Scan(&balance)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT record_type, amount, voided, affect_balance FROM gold_records WHERE resident_code = ?", code)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]any{
		"balance":  balance,
		"in":       int64(0),
		"out":      int64(0),
		"forfeit":  int64(0),
		"allocate": int64(0),
		"net":      int64(0),
		"count":    0,
		"voided":   0,
	}
	for rows.Next() {
		var typ string
		var amount int64
		var voided, affect int
		if err := rows.Scan(&typ, &amount, &voided, &affect); err != nil {
			return nil, err
		}
		result["count"] = result["count"].(int) + 1
		if voided != 0 {
			result["voided"] = result["voided"].(int) + 1
			continue
		}
		switch typ {
		case GoldIn:
			result["in"] = result["in"].(int64) + abs64(amount)
		case GoldOut:
			result["out"] = result["out"].(int64) + abs64(amount)
		case GoldForfeit:
			result["forfeit"] = result["forfeit"].(int64) + abs64(amount)
		case GoldAllocate:
			result["allocate"] = result["allocate"].(int64) + abs64(amount)
		}
		if affect != 0 {
			result["net"] = result["net"].(int64) + amount
		}
	}
	return result, rows.Err()
}

type residentRow struct {
	Code     string
	Name     string
	Kind     string
	Balance  int64
	Identity string
	Remark   string
}

func (r residentRow) toRole(history []IdentityDTO, historyTexts []string) RoleDTO {
	return RoleDTO{
		ID:              r.Code,
		Name:            r.Name,
		Code:            r.Code,
		Type:            r.Kind,
		Balance:         r.Balance,
		IdentityCurrent: r.Identity,
		IdentityHistory: history,
		IdentityTexts:   historyTexts,
		Remark:          r.Remark,
	}
}

func (s *Store) ensureResidentTx(ctx context.Context, tx *sql.Tx, code, name, kind string, balance int64, identity, remark, now string) error {
	var existingKind string
	err := tx.QueryRowContext(ctx, "SELECT kind FROM residents WHERE code = ?", code).Scan(&existingKind)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `INSERT INTO residents(code, name, kind, balance, identity_current, remark, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, code, name, kind, balance, identity, remark, now, now)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO identity_history(resident_code, resident_name_snapshot, identity, occurred_at)
			VALUES(?, ?, ?, ?)`, code, name, identity, now)
		return err
	}
	if err != nil {
		return err
	}
	if existingKind != kind {
		return fmt.Errorf("%w: resident code already belongs to %s", ErrConflict, existingKind)
	}
	_, err = tx.ExecContext(ctx, `UPDATE residents
		SET name = ?, remark = CASE WHEN ? != '' THEN ? ELSE remark END, updated_at = ?
		WHERE code = ?`, name, remark, remark, now, code)
	return err
}

func appendProfileChangeNotes(remark string, notes []string) string {
	clean := make([]string, 0, len(notes))
	for _, note := range notes {
		note = strings.TrimSpace(note)
		if note != "" {
			clean = append(clean, note)
		}
	}
	if len(clean) == 0 {
		return remark
	}
	if remark == "" {
		return strings.Join(clean, "；")
	}
	return remark + "；" + strings.Join(clean, "；")
}
func (s *Store) setIdentityIfChangedTx(ctx context.Context, tx *sql.Tx, code, identity, operator, now string) error {
	var name, current string
	err := tx.QueryRowContext(ctx, "SELECT name, identity_current FROM residents WHERE code = ?", code).Scan(&name, &current)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if current == identity {
		return nil
	}
	if _, err := tx.ExecContext(ctx, "UPDATE residents SET identity_current = ?, updated_at = ? WHERE code = ?", identity, now, code); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO identity_history(resident_code, resident_name_snapshot, identity, occurred_at)
		VALUES(?, ?, ?, ?)`, code, name, identity, now); err != nil {
		return err
	}
	return s.insertAudit(ctx, tx, "identity.set", fmt.Sprintf("身份变更：%s(%s) -> %s", name, code, identity), operator, now)
}

func (s *Store) residentTx(ctx context.Context, tx *sql.Tx, code string) (residentRow, error) {
	var row residentRow
	err := tx.QueryRowContext(ctx, "SELECT code, name, kind, balance, identity_current, remark FROM residents WHERE code = ?", code).
		Scan(&row.Code, &row.Name, &row.Kind, &row.Balance, &row.Identity, &row.Remark)
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrNotFound
	}
	return row, err
}

func (s *Store) currentSessionTx(ctx context.Context, tx *sql.Tx) (*CitySession, error) {
	var session CitySession
	var closed sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT opened_at, closed_at, operator FROM city_sessions
		WHERE closed_at IS NULL ORDER BY opened_at DESC LIMIT 1`).Scan(&session.OpenedAt, &closed, &session.Operator)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: no active city session", ErrBadRequest)
	}
	if err != nil {
		return nil, err
	}
	if closed.Valid {
		session.ClosedAt = closed.String
	}
	return &session, nil
}

func (s *Store) currentSession(ctx context.Context) (*CitySession, error) {
	var session CitySession
	var closed sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT opened_at, closed_at, operator FROM city_sessions
		WHERE closed_at IS NULL ORDER BY opened_at DESC LIMIT 1`).Scan(&session.OpenedAt, &closed, &session.Operator)
	if err != nil {
		return nil, err
	}
	if closed.Valid {
		session.ClosedAt = closed.String
	}
	return &session, nil
}

func (s *Store) insertAudit(ctx context.Context, tx *sql.Tx, kind, message, operator, now string) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO audit_events(kind, message, operator, created_at) VALUES(?, ?, ?, ?)", kind, message, operator, now)
	return err
}

func (s *Store) loadResidents(ctx context.Context) ([]residentRow, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT code, name, kind, balance, identity_current, remark FROM residents ORDER BY kind, code")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []residentRow
	for rows.Next() {
		var row residentRow
		if err := rows.Scan(&row.Code, &row.Name, &row.Kind, &row.Balance, &row.Identity, &row.Remark); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) loadIdentityHistory(ctx context.Context) (map[string][]IdentityDTO, map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, resident_code, resident_name_snapshot, identity, occurred_at, deleted_at
		FROM identity_history WHERE deleted_at IS NULL ORDER BY occurred_at DESC, id DESC`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	items := map[string][]IdentityDTO{}
	texts := map[string][]string{}
	for rows.Next() {
		var item IdentityDTO
		var deleted sql.NullString
		if err := rows.Scan(&item.ID, &item.Code, &item.Name, &item.Identity, &item.Occurred, &deleted); err != nil {
			return nil, nil, err
		}
		if deleted.Valid {
			item.DeletedAt = deleted.String
		}
		item.Display = s.formatDisplayTime(item.Occurred) + " 为 " + item.Identity
		items[item.Code] = append(items[item.Code], item)
		texts[item.Code] = append(texts[item.Code], item.Display)
	}
	return items, texts, rows.Err()
}

func (s *Store) loadIdentityHistoryForCodes(ctx context.Context, codes []string) (map[string][]IdentityDTO, map[string][]string, error) {
	items := map[string][]IdentityDTO{}
	texts := map[string][]string{}
	codes = uniqueNonEmptyCodes(codes)
	if len(codes) == 0 {
		return items, texts, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(codes)), ",")
	args := make([]any, 0, len(codes))
	for _, code := range codes {
		args = append(args, code)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, resident_code, resident_name_snapshot, identity, occurred_at, deleted_at
		FROM identity_history
		WHERE deleted_at IS NULL AND resident_code IN (`+placeholders+`)
		ORDER BY occurred_at DESC, id DESC`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item IdentityDTO
		var deleted sql.NullString
		if err := rows.Scan(&item.ID, &item.Code, &item.Name, &item.Identity, &item.Occurred, &deleted); err != nil {
			return nil, nil, err
		}
		if deleted.Valid {
			item.DeletedAt = deleted.String
		}
		item.Display = s.formatDisplayTime(item.Occurred) + " 为 " + item.Identity
		items[item.Code] = append(items[item.Code], item)
		texts[item.Code] = append(texts[item.Code], item.Display)
	}
	return items, texts, rows.Err()
}

type residentSearchOptions struct {
	Kind      string
	CodeExact string
	CodeLike  string
	NameLike  string
	Limit     int
	MatchAny  bool
}

func (s *Store) searchResidentRows(ctx context.Context, opts residentSearchOptions) ([]RoleDTO, error) {
	if opts.Limit <= 0 || opts.Limit > 50 {
		opts.Limit = 10
	}
	clauses := []string{"1 = 1"}
	args := []any{}
	if opts.Kind != "" {
		clauses = append(clauses, "kind = ?")
		args = append(args, opts.Kind)
	}
	if opts.MatchAny {
		var matchClauses []string
		if opts.CodeExact != "" {
			matchClauses = append(matchClauses, "code = ?")
			args = append(args, opts.CodeExact)
		}
		if opts.CodeLike != "" {
			matchClauses = append(matchClauses, "code LIKE ? ESCAPE '\\'")
			args = append(args, "%"+escapeLike(opts.CodeLike)+"%")
		}
		if opts.NameLike != "" {
			matchClauses = append(matchClauses, "name LIKE ? ESCAPE '\\'")
			args = append(args, "%"+escapeLike(opts.NameLike)+"%")
		}
		if len(matchClauses) > 0 {
			clauses = append(clauses, "("+strings.Join(matchClauses, " OR ")+")")
		}
	} else {
		if opts.CodeExact != "" {
			clauses = append(clauses, "code = ?")
			args = append(args, opts.CodeExact)
		}
		if opts.CodeLike != "" {
			clauses = append(clauses, "code LIKE ? ESCAPE '\\'")
			args = append(args, "%"+escapeLike(opts.CodeLike)+"%")
		}
		if opts.NameLike != "" {
			clauses = append(clauses, "name LIKE ? ESCAPE '\\'")
			args = append(args, "%"+escapeLike(opts.NameLike)+"%")
		}
	}
	args = append(args, opts.Limit)
	rows, err := s.db.QueryContext(ctx, `SELECT code, name, kind, balance, identity_current, remark
		FROM residents
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY kind, code
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var residents []residentRow
	var codes []string
	for rows.Next() {
		var row residentRow
		if err := rows.Scan(&row.Code, &row.Name, &row.Kind, &row.Balance, &row.Identity, &row.Remark); err != nil {
			return nil, err
		}
		residents = append(residents, row)
		codes = append(codes, row.Code)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	history, historyTexts, err := s.loadIdentityHistoryForCodes(ctx, codes)
	if err != nil {
		return nil, err
	}
	out := make([]RoleDTO, 0, len(residents))
	for _, resident := range residents {
		out = append(out, resident.toRole(history[resident.Code], historyTexts[resident.Code]))
	}
	return out, nil
}

func (s *Store) loadVisibleNPCRoles(ctx context.Context) ([]RoleDTO, []string, []string, error) {
	args := []any{KindNPC}
	defaultFilter := "0"
	if len(DefaultVisibleNPCCodes) > 0 {
		defaultFilter = "r.code IN (" + strings.TrimRight(strings.Repeat("?,", len(DefaultVisibleNPCCodes)), ",") + ")"
		for _, code := range DefaultVisibleNPCCodes {
			args = append(args, code)
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT r.code, r.name, r.kind, r.balance, r.identity_current, r.remark
		FROM residents r
		LEFT JOIN npc_panel_state p ON p.resident_code = r.code
		WHERE r.kind = ? AND (p.visible = 1 OR (p.resident_code IS NULL AND `+defaultFilter+`))
		ORDER BY r.code`, args...)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	var residents []residentRow
	var codes []string
	for rows.Next() {
		var row residentRow
		if err := rows.Scan(&row.Code, &row.Name, &row.Kind, &row.Balance, &row.Identity, &row.Remark); err != nil {
			return nil, nil, nil, err
		}
		residents = append(residents, row)
		codes = append(codes, row.Code)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	history, historyTexts, err := s.loadIdentityHistoryForCodes(ctx, codes)
	if err != nil {
		return nil, nil, nil, err
	}
	roles := make([]RoleDTO, 0, len(residents))
	visibleCodes := make([]string, 0, len(residents))
	for _, resident := range residents {
		roles = append(roles, resident.toRole(history[resident.Code], historyTexts[resident.Code]))
		visibleCodes = append(visibleCodes, resident.Code)
	}
	return roles, visibleCodes, []string{}, nil
}

func (s *Store) loadGoldRecords(ctx context.Context) ([]GoldRecordDTO, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, resident_code, resident_name_snapshot, identity_snapshot, record_type, amount,
		balance_after, remark, affect_balance, voided, operator, occurred_at
		FROM gold_records ORDER BY occurred_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return s.scanGoldRecords(rows)
}

func (s *Store) loadCurrentSessionGoldRecords(ctx context.Context, session *CitySession) ([]GoldRecordDTO, error) {
	if session == nil {
		return []GoldRecordDTO{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, resident_code, resident_name_snapshot, identity_snapshot, record_type, amount,
		balance_after, remark, affect_balance, voided, operator, occurred_at
		FROM gold_records
		WHERE occurred_at >= ?
		ORDER BY occurred_at DESC, id DESC`, session.OpenedAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return s.scanGoldRecords(rows)
}

func (s *Store) scanGoldRecords(rows *sql.Rows) ([]GoldRecordDTO, error) {
	var out []GoldRecordDTO
	for rows.Next() {
		var rec GoldRecordDTO
		var typ string
		var affect, voided int
		var occurred string
		if err := rows.Scan(&rec.ID, &rec.Code, &rec.Name, &rec.Identity, &typ, &rec.Amount, &rec.Balance, &rec.Remark, &affect, &voided, &rec.Operator, &occurred); err != nil {
			return nil, err
		}
		rec.RoleID = rec.Code
		rec.TypeCode = typ
		rec.Type = goldTypeLabel(typ)
		rec.AffectBalance = affect != 0
		rec.Voided = voided != 0
		rec.Time = s.formatDisplayTime(occurred)
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *Store) loadNPCPanel(ctx context.Context, residents []residentRow) ([]string, []string, error) {
	visible := []string{}
	hiddenKeys := []string{}
	for _, resident := range residents {
		if resident.Kind != KindNPC {
			continue
		}
		if s.isNPCVisible(ctx, resident.Code) {
			visible = append(visible, resident.Code)
		} else {
			hiddenKeys = append(hiddenKeys, resident.Code+"\x00"+resident.Name)
		}
	}
	return visible, hiddenKeys, nil
}

func (s *Store) isNPCVisible(ctx context.Context, code string) bool {
	var visible int
	err := s.db.QueryRowContext(ctx, "SELECT visible FROM npc_panel_state WHERE resident_code = ?", code).Scan(&visible)
	if err == nil {
		return visible != 0
	}
	for _, defaultCode := range DefaultVisibleNPCCodes {
		if defaultCode == code {
			return true
		}
	}
	return false
}

func (s *Store) loadTravelRoles(ctx context.Context, sessionOpenedAt string) ([]RoleDTO, []string, []string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.id, t.resident_code, t.resident_name_snapshot, t.identity_snapshot,
		t.enter_at, t.leave_at, t.stay_minutes, t.canceled_at, t.hidden_at,
		COALESCE(r.name, t.resident_name_snapshot), COALESCE(r.kind, ?), COALESCE(r.balance, 0),
		COALESCE(r.identity_current, t.identity_snapshot), COALESCE(r.remark, '')
		FROM travel_records t
		LEFT JOIN residents r ON r.code = t.resident_code
		WHERE t.session_opened_at = ?
		ORDER BY t.enter_at ASC, t.id ASC`, KindPlayer, sessionOpenedAt)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	var roles []RoleDTO
	var codes []string
	var hidden []string
	var suppressed []string
	for rows.Next() {
		var id int64
		var code, nameSnapshot, identitySnapshot, enterAt, leaveAt string
		var name, kind, identity, remark string
		var balance int64
		var stayMinutes int
		var canceled, hiddenAt sql.NullString
		if err := rows.Scan(&id, &code, &nameSnapshot, &identitySnapshot, &enterAt, &leaveAt, &stayMinutes, &canceled, &hiddenAt, &name, &kind, &balance, &identity, &remark); err != nil {
			return nil, nil, nil, err
		}
		if canceled.Valid {
			hidden = append(hidden, code)
			suppressed = append(suppressed, code)
			continue
		}
		if hiddenAt.Valid {
			hidden = append(hidden, code)
			continue
		}
		extensions, err := s.loadTravelExtensions(ctx, id)
		if err != nil {
			return nil, nil, nil, err
		}
		role := residentRow{Code: code, Name: name, Kind: kind, Balance: balance, Identity: identity, Remark: remark}.toRole(nil, nil)
		role.Type = KindPlayer
		role.ID = code
		role.TravelID = id
		role.EnterTime = enterAt
		role.LeaveTime = leaveAt
		role.StayHours = float64(stayMinutes) / 60.0
		role.TimeIncreaseLog = extensions
		roles = append(roles, role)
		codes = append(codes, code)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	history, historyTexts, err := s.loadIdentityHistoryForCodes(ctx, codes)
	if err != nil {
		return nil, nil, nil, err
	}
	for i := range roles {
		code := roles[i].Code
		roles[i].IdentityHistory = history[code]
		roles[i].IdentityTexts = historyTexts[code]
	}
	return roles, hidden, suppressed, nil
}

func (s *Store) loadTravelExtensions(ctx context.Context, travelID int64) ([]TimeIncreaseLog, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, added_minutes, occurred_at FROM travel_extensions WHERE travel_id = ? ORDER BY occurred_at DESC, id DESC", travelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TimeIncreaseLog
	for rows.Next() {
		var log TimeIncreaseLog
		var occurred string
		if err := rows.Scan(&log.ID, &log.Minutes, &occurred); err != nil {
			return nil, err
		}
		log.Time = s.formatDisplayTime(occurred)
		out = append(out, log)
	}
	return out, rows.Err()
}

func (s *Store) todayStats(ctx context.Context) (TodayStats, error) {
	now := s.now().In(s.loc)
	stats := TodayStats{}
	session, err := s.currentSession(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return stats, nil
	}
	if err != nil {
		return stats, err
	}
	nowText := now.Format(time.RFC3339)
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM travel_records
		WHERE session_opened_at = ? AND canceled_at IS NULL`, session.OpenedAt).Scan(&stats.TodayEntered)
	if err != nil {
		return stats, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM travel_records
		WHERE session_opened_at = ? AND canceled_at IS NULL AND hidden_at IS NULL AND leave_at > ?`, session.OpenedAt, nowText).Scan(&stats.CurrentInCity)
	if err != nil {
		return stats, err
	}
	var dailyExpense sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT SUM(CASE
			WHEN record_type = ? THEN -ABS(amount)
			WHEN record_type IN (?, ?) THEN ABS(amount)
			ELSE 0
		END)
		FROM gold_records
		WHERE voided = 0 AND occurred_at >= ?`, GoldIn, GoldOut, GoldAllocate, session.OpenedAt).Scan(&dailyExpense)
	if err != nil {
		return stats, err
	}
	if dailyExpense.Valid {
		stats.DailyExpense = dailyExpense.Int64
	}
	return stats, nil
}

func (s *Store) latestAudit(ctx context.Context) (*LatestOperation, error) {
	var op LatestOperation
	var created string
	err := s.db.QueryRowContext(ctx, "SELECT created_at, message FROM audit_events ORDER BY id DESC LIMIT 1").Scan(&created, &op.Content)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	op.Time = s.formatDisplayTime(created)
	return &op, nil
}

func parseOpenCityTime(raw string, loc *time.Location) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, errors.New("open city time is required")
	}
	t, err := parseDBTime(raw, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid open city time %q", raw)
	}
	return t.In(loc), nil
}
func parseEnterTime(raw string, now time.Time, loc *time.Location) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, errors.New("enter time is required")
	}
	if t, err := parseDBTime(raw, loc); err == nil {
		return t.In(loc), nil
	}
	if strings.Contains(raw, ":") {
		parts := strings.Split(raw, ":")
		if len(parts) >= 2 {
			hour, err1 := strconv.Atoi(parts[0])
			minute, err2 := strconv.Atoi(parts[1])
			if err1 == nil && err2 == nil && hour >= 0 && hour <= 23 && minute >= 0 && minute <= 59 {
				return time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, loc), nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("invalid enter time %q", raw)
}

func normalizeIdentity(identity string) string {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return DefaultIdentity
	}
	return identity
}

func validGoldType(typ string) bool {
	switch typ {
	case GoldIn, GoldOut, GoldForfeit, GoldAllocate:
		return true
	default:
		return false
	}
}

func signedGoldAmount(typ string, amount int64) int64 {
	switch typ {
	case GoldOut, GoldForfeit:
		return -abs64(amount)
	default:
		return abs64(amount)
	}
}

func goldTypeLabel(typ string) string {
	switch typ {
	case GoldIn:
		return "存入"
	case GoldOut:
		return "取出"
	case GoldForfeit:
		return "罚没"
	case GoldAllocate:
		return "拨付"
	default:
		return typ
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func abs64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func uniqueNonEmptyCodes(codes []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(codes))
	for _, code := range codes {
		code = normalizeCode(code)
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	return out
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

func sameLocalDay(a, b time.Time, loc *time.Location) bool {
	a = a.In(loc)
	b = b.In(loc)
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}

func pathCode(raw string) string {
	value, err := url.PathUnescape(raw)
	if err != nil {
		return normalizeCode(raw)
	}
	return normalizeCode(value)
}
