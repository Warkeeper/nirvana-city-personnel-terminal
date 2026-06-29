package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

var mergeRequiredSheets = []string{
	"数据库",
	"金条流水",
	"玩家进出城记录",
	"身份历史",
	"时长增加记录",
	"开城记录",
	"已取消进出城记录",
}

func MergeSheetNames() []string {
	out := make([]string, len(mergeRequiredSheets))
	copy(out, mergeRequiredSheets)
	return out
}

type MergeReport struct {
	Source     string                     `json:"source"`
	BackupPath string                     `json:"backupPath,omitempty"`
	Sheets     map[string]MergeSheetStats `json:"sheets"`
}

type MergeSheetStats struct {
	Inserted int `json:"inserted"`
	Updated  int `json:"updated"`
	Skipped  int `json:"skipped"`
	Errors   int `json:"errors"`
}

func (r *MergeReport) addInserted(sheet string) {
	stats := r.Sheets[sheet]
	stats.Inserted++
	r.Sheets[sheet] = stats
}

func (r *MergeReport) addUpdated(sheet string) {
	stats := r.Sheets[sheet]
	stats.Updated++
	r.Sheets[sheet] = stats
}

func (r *MergeReport) addSkipped(sheet string) {
	stats := r.Sheets[sheet]
	stats.Skipped++
	r.Sheets[sheet] = stats
}

func (s *Store) MergeWorkbook(ctx context.Context, path string) (*MergeReport, error) {
	book, err := ReadXLSX(path)
	if err != nil {
		return nil, err
	}
	if err := validateMergeWorkbook(book); err != nil {
		return nil, err
	}
	report := &MergeReport{Source: path, Sheets: map[string]MergeSheetStats{}}
	for _, sheet := range mergeRequiredSheets {
		report.Sheets[sheet] = MergeSheetStats{}
	}
	backup, err := s.Backup(ctx, "merge")
	if err != nil {
		return nil, fmt.Errorf("merge backup failed: %w", err)
	}
	report.BackupPath = backup
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		sourceSessions, err := s.mergeSessions(ctx, tx, book.Sheets["开城记录"], report)
		if err != nil {
			return err
		}
		if err := s.mergeResidents(ctx, tx, book.Sheets["数据库"], report); err != nil {
			return err
		}
		if err := s.mergeIdentityHistory(ctx, tx, book.Sheets["身份历史"], report); err != nil {
			return err
		}
		if err := s.mergeGoldRecords(ctx, tx, book.Sheets["金条流水"], report); err != nil {
			return err
		}
		if err := s.mergeTravelRows(ctx, tx, book.Sheets["玩家进出城记录"], sourceSessions, false, report); err != nil {
			return err
		}
		if err := s.mergeTravelRows(ctx, tx, book.Sheets["已取消进出城记录"], sourceSessions, true, report); err != nil {
			return err
		}
		if err := s.mergeExtensionSheet(ctx, tx, book.Sheets["时长增加记录"], report); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return report, nil
}

func validateMergeWorkbook(book *XLSXWorkbook) error {
	if err := requireSheets(book, mergeRequiredSheets); err != nil {
		return err
	}
	if len(book.Sheets) != len(mergeRequiredSheets) {
		allowed := map[string]bool{}
		for _, sheet := range mergeRequiredSheets {
			allowed[sheet] = true
		}
		var extras []string
		for sheet := range book.Sheets {
			if !allowed[sheet] {
				extras = append(extras, sheet)
			}
		}
		if len(extras) > 0 {
			return fmt.Errorf("unsupported workbook format: unexpected sheet(s): %s", strings.Join(extras, ", "))
		}
		return fmt.Errorf("unsupported workbook format: expected %d sheet(s), got %d", len(mergeRequiredSheets), len(book.Sheets))
	}
	requiredHeaders := map[string][]string{
		"数据库":      {"姓名", "编号", "金条余额", "常驻居民/城邦居民", "当前身份", "历史身份记录", "备注"},
		"金条流水":     {"时间", "编号", "姓名", "当前身份", "类型", "数量", "操作后余额", "备注", "状态", "操作员"},
		"玩家进出城记录":  {"开城时间", "姓名", "编号", "当前身份", "进城时间", "离城时间", "时长增加记录", "操作员"},
		"身份历史":     {"时间", "编号", "姓名快照", "身份", "状态"},
		"时长增加记录":   {"进出城记录ID", "编号", "姓名", "增加分钟", "操作时间", "操作员"},
		"开城记录":     {"开城时间", "关城时间", "操作员", "备注"},
		"已取消进出城记录": {"开城时间", "姓名", "编号", "当前身份", "进城时间", "离城时间", "取消时间", "操作员"},
	}
	for sheet, headers := range requiredHeaders {
		if err := requireHeaders(book.Sheets[sheet], headers); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) mergeResidents(ctx context.Context, tx *sql.Tx, sheet *XLSXSheet, report *MergeReport) error {
	for _, row := range sheet.Rows {
		code, err := requireTextCodeCell(sheet.Name, row, "编号")
		if err != nil {
			return err
		}
		name := textCell(row, "姓名")
		if name == "" {
			return mergeCellError(sheet.Name, row.Index, "姓名", "不能为空")
		}
		balance, err := parseMergeInt(sheet.Name, row, "金条余额")
		if err != nil {
			return err
		}
		kind, err := parseMergeResidentKind(sheet.Name, row)
		if err != nil {
			return err
		}
		identity := normalizeIdentity(textCell(row, "当前身份"))
		remark := textCell(row, "备注")
		now := s.nowString()
		var existingKind string
		err = tx.QueryRowContext(ctx, "SELECT kind FROM residents WHERE code = ?", code).Scan(&existingKind)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO residents(code, name, kind, balance, identity_current, remark, created_at, updated_at)
				VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, code, name, kind, balance, identity, remark, now, now); err != nil {
				return err
			}
			report.addInserted(sheet.Name)
			continue
		}
		if err != nil {
			return err
		}
		if existingKind != kind {
			return mergeCellError(sheet.Name, row.Index, "常驻居民/城邦居民", fmt.Sprintf("编号 %s 已存在为 %s，不能合并为 %s", code, existingKind, kind))
		}
		if _, err := tx.ExecContext(ctx, `UPDATE residents
			SET name = ?, balance = ?, identity_current = ?, remark = ?, updated_at = ?
			WHERE code = ?`, name, balance, identity, remark, now, code); err != nil {
			return err
		}
		report.addUpdated(sheet.Name)
	}
	return nil
}

func (s *Store) mergeSessions(ctx context.Context, tx *sql.Tx, sheet *XLSXSheet, report *MergeReport) (map[string]bool, error) {
	sourceSessions := map[string]bool{}
	for _, row := range sheet.Rows {
		openedAt, err := parseMergeTime(s, sheet.Name, row, "开城时间", true)
		if err != nil {
			return nil, err
		}
		closedAt, err := parseMergeTime(s, sheet.Name, row, "关城时间", false)
		if err != nil {
			return nil, err
		}
		operator := textCell(row, "操作员")
		if operator == "" {
			return nil, mergeCellError(sheet.Name, row.Index, "操作员", "不能为空")
		}
		note := textCell(row, "备注")
		var existing string
		err = tx.QueryRowContext(ctx, "SELECT opened_at FROM city_sessions WHERE opened_at = ?", openedAt).Scan(&existing)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := tx.ExecContext(ctx, "INSERT INTO city_sessions(opened_at, closed_at, operator, note) VALUES(?, ?, ?, ?)", openedAt, nullableString(closedAt), operator, note); err != nil {
				return nil, err
			}
			report.addInserted(sheet.Name)
		} else if err != nil {
			return nil, err
		} else {
			if _, err := tx.ExecContext(ctx, "UPDATE city_sessions SET closed_at = ?, operator = ?, note = ? WHERE opened_at = ?", nullableString(closedAt), operator, note, openedAt); err != nil {
				return nil, err
			}
			report.addUpdated(sheet.Name)
		}
		sourceSessions[openedAt] = true
	}
	return sourceSessions, nil
}

func (s *Store) mergeIdentityHistory(ctx context.Context, tx *sql.Tx, sheet *XLSXSheet, report *MergeReport) error {
	for _, row := range sheet.Rows {
		code, err := requireTextCodeCell(sheet.Name, row, "编号")
		if err != nil {
			return err
		}
		if err := requireResidentExists(ctx, tx, sheet.Name, row.Index, code); err != nil {
			return err
		}
		occurredAt, err := parseMergeTime(s, sheet.Name, row, "时间", true)
		if err != nil {
			return err
		}
		name := textCell(row, "姓名快照")
		if name == "" {
			return mergeCellError(sheet.Name, row.Index, "姓名快照", "不能为空")
		}
		identity := textCell(row, "身份")
		if identity == "" {
			return mergeCellError(sheet.Name, row.Index, "身份", "不能为空")
		}
		deletedAt, err := parseMergeDeletedAt(s, sheet.Name, row, "状态", occurredAt)
		if err != nil {
			return err
		}
		var id int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM identity_history
			WHERE resident_code = ? AND occurred_at = ? AND identity = ?
			ORDER BY id LIMIT 1`, code, occurredAt, identity).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO identity_history(resident_code, resident_name_snapshot, identity, occurred_at, deleted_at)
				VALUES(?, ?, ?, ?, ?)`, code, name, identity, occurredAt, nullableString(deletedAt)); err != nil {
				return err
			}
			report.addInserted(sheet.Name)
			continue
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE identity_history SET resident_name_snapshot = ?, deleted_at = ? WHERE id = ?", name, nullableString(deletedAt), id); err != nil {
			return err
		}
		report.addUpdated(sheet.Name)
	}
	return nil
}

func (s *Store) mergeGoldRecords(ctx context.Context, tx *sql.Tx, sheet *XLSXSheet, report *MergeReport) error {
	for _, row := range sheet.Rows {
		code, err := requireTextCodeCell(sheet.Name, row, "编号")
		if err != nil {
			return err
		}
		if err := requireResidentExists(ctx, tx, sheet.Name, row.Index, code); err != nil {
			return err
		}
		occurredAt, err := parseMergeTime(s, sheet.Name, row, "时间", true)
		if err != nil {
			return err
		}
		recordType, err := parseMergeGoldType(sheet.Name, row)
		if err != nil {
			return err
		}
		amount, err := parseMergeInt(sheet.Name, row, "数量")
		if err != nil {
			return err
		}
		balance, err := parseMergeInt(sheet.Name, row, "操作后余额")
		if err != nil {
			return err
		}
		name := textCell(row, "姓名")
		identity := textCell(row, "当前身份")
		remark := textCell(row, "备注")
		operator := textCell(row, "操作员")
		voided, err := parseMergeVoided(sheet.Name, row)
		if err != nil {
			return err
		}
		affectBalance := recordType != GoldAllocate
		balanceReverted := voided && affectBalance
		var id int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM gold_records
			WHERE resident_code = ? AND occurred_at = ? AND record_type = ? AND amount = ?
			ORDER BY id LIMIT 1`, code, occurredAt, recordType, amount).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO gold_records(
				resident_code, resident_name_snapshot, identity_snapshot, record_type, amount,
				balance_after, remark, affect_balance, voided, balance_reverted, operator, occurred_at
			) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				code, name, identity, recordType, amount, balance, remark, boolInt(affectBalance), boolInt(voided), boolInt(balanceReverted), operator, occurredAt); err != nil {
				return err
			}
			report.addInserted(sheet.Name)
			continue
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE gold_records
			SET resident_name_snapshot = ?, identity_snapshot = ?, balance_after = ?, remark = ?,
				affect_balance = ?, voided = ?, balance_reverted = ?, operator = ?
			WHERE id = ?`,
			name, identity, balance, remark, boolInt(affectBalance), boolInt(voided), boolInt(balanceReverted), operator, id); err != nil {
			return err
		}
		report.addUpdated(sheet.Name)
	}
	return nil
}

func (s *Store) mergeTravelRows(ctx context.Context, tx *sql.Tx, sheet *XLSXSheet, sourceSessions map[string]bool, canceled bool, report *MergeReport) error {
	for _, row := range sheet.Rows {
		code, err := requireTextCodeCell(sheet.Name, row, "编号")
		if err != nil {
			return err
		}
		if err := requireResidentExists(ctx, tx, sheet.Name, row.Index, code); err != nil {
			return err
		}
		sessionOpenedAt, err := parseMergeTime(s, sheet.Name, row, "开城时间", true)
		if err != nil {
			return err
		}
		if !sourceSessions[sessionOpenedAt] {
			return mergeCellError(sheet.Name, row.Index, "开城时间", fmt.Sprintf("未找到对应开城记录 %s", textCell(row, "开城时间")))
		}
		name := textCell(row, "姓名")
		identity := textCell(row, "当前身份")
		enterAt, err := parseMergeTime(s, sheet.Name, row, "进城时间", true)
		if err != nil {
			return err
		}
		leaveAt, err := parseMergeTime(s, sheet.Name, row, "离城时间", true)
		if err != nil {
			return err
		}
		stayMinutes, err := minutesBetween(s.loc, enterAt, leaveAt)
		if err != nil {
			return mergeCellError(sheet.Name, row.Index, "离城时间", err.Error())
		}
		operator := textCell(row, "操作员")
		var canceledAt string
		if canceled {
			canceledAt, err = parseMergeTime(s, sheet.Name, row, "取消时间", true)
			if err != nil {
				return err
			}
		}
		var id int64
		err = tx.QueryRowContext(ctx, `SELECT id FROM travel_records
			WHERE session_opened_at = ? AND resident_code = ? AND enter_at = ?
			ORDER BY id LIMIT 1`, sessionOpenedAt, code, enterAt).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			res, err := tx.ExecContext(ctx, `INSERT INTO travel_records(
				session_opened_at, resident_code, resident_name_snapshot, identity_snapshot, enter_at, leave_at,
				stay_minutes, canceled_at, hidden_at, hidden_after_leave, operator, created_at
			) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
				sessionOpenedAt, code, name, identity, enterAt, leaveAt, stayMinutes, nullableString(canceledAt), nullableString(canceledAt), operator, s.nowString())
			if err != nil {
				return err
			}
			id, err = res.LastInsertId()
			if err != nil {
				return err
			}
			report.addInserted(sheet.Name)
		} else if err != nil {
			return err
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE travel_records
				SET resident_name_snapshot = ?, identity_snapshot = ?, leave_at = ?, stay_minutes = ?,
					canceled_at = ?, hidden_at = ?, hidden_after_leave = 0, operator = ?
				WHERE id = ?`,
				name, identity, leaveAt, stayMinutes, nullableString(canceledAt), nullableString(canceledAt), operator, id); err != nil {
				return err
			}
			report.addUpdated(sheet.Name)
		}
		if !canceled {
			if err := s.mergeInlineTravelExtensions(ctx, tx, id, sheet, row, operator, report); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) mergeInlineTravelExtensions(ctx context.Context, tx *sql.Tx, travelID int64, sheet *XLSXSheet, row XLSXRow, operator string, report *MergeReport) error {
	raw := textCell(row, "时长增加记录")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	for _, part := range parts {
		occurredAt, minutes, err := parseInlineExtension(s, sheet.Name, row.Index, strings.TrimSpace(part))
		if err != nil {
			return err
		}
		inserted, err := mergeTravelExtensionWithResult(ctx, tx, travelID, occurredAt, minutes, operator)
		if err != nil {
			return err
		}
		if inserted {
			report.addInserted("时长增加记录")
		} else {
			report.addUpdated("时长增加记录")
		}
	}
	return nil
}

func (s *Store) mergeExtensionSheet(ctx context.Context, tx *sql.Tx, sheet *XLSXSheet, report *MergeReport) error {
	for _, row := range sheet.Rows {
		code, err := requireTextCodeCell(sheet.Name, row, "编号")
		if err != nil {
			return err
		}
		if err := requireResidentExists(ctx, tx, sheet.Name, row.Index, code); err != nil {
			return err
		}
		occurredAt, err := parseMergeTime(s, sheet.Name, row, "操作时间", true)
		if err != nil {
			return err
		}
		minutes, err := parseMergeInt(sheet.Name, row, "增加分钟")
		if err != nil {
			return err
		}
		if minutes <= 0 || minutes > int64(math.MaxInt32) {
			return mergeCellError(sheet.Name, row.Index, "增加分钟", "必须是正整数")
		}
		travelID, err := findTravelForExtension(ctx, tx, s.loc, code, occurredAt)
		if err != nil {
			return mergeCellError(sheet.Name, row.Index, "进出城记录ID", err.Error())
		}
		inserted, err := mergeTravelExtensionWithResult(ctx, tx, travelID, occurredAt, int(minutes), textCell(row, "操作员"))
		if err != nil {
			return err
		}
		if inserted {
			report.addInserted(sheet.Name)
		} else {
			report.addUpdated(sheet.Name)
		}
	}
	return nil
}

func mergeTravelExtensionWithResult(ctx context.Context, tx *sql.Tx, travelID int64, occurredAt string, minutes int, operator string) (bool, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM travel_extensions
		WHERE travel_id = ? AND occurred_at = ? AND added_minutes = ?
		ORDER BY id LIMIT 1`, travelID, occurredAt, minutes).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, "INSERT INTO travel_extensions(travel_id, added_minutes, occurred_at, operator) VALUES(?, ?, ?, ?)", travelID, minutes, occurredAt, operator)
		return true, err
	}
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE travel_extensions SET operator = ? WHERE id = ?", operator, id)
	return false, err
}

func findTravelForExtension(ctx context.Context, tx *sql.Tx, loc *time.Location, code string, occurredAt string) (int64, error) {
	occurred, err := parseDBTime(occurredAt, loc)
	if err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id, enter_at, leave_at FROM travel_records WHERE resident_code = ? AND canceled_at IS NULL", code)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var matches []int64
	for rows.Next() {
		var id int64
		var enterRaw, leaveRaw string
		if err := rows.Scan(&id, &enterRaw, &leaveRaw); err != nil {
			return 0, err
		}
		enterAt, err := parseDBTime(enterRaw, loc)
		if err != nil {
			return 0, err
		}
		leaveAt, err := parseDBTime(leaveRaw, loc)
		if err != nil {
			return 0, err
		}
		if !occurred.Before(enterAt) && !occurred.After(leaveAt) {
			matches = append(matches, id)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(matches) == 0 {
		return 0, fmt.Errorf("找不到编号 %s 在操作时间内的进出城记录", code)
	}
	if len(matches) > 1 {
		return 0, fmt.Errorf("编号 %s 在操作时间内匹配到多条进出城记录", code)
	}
	return matches[0], nil
}

func requireResidentExists(ctx context.Context, tx *sql.Tx, sheet string, row int, code string) error {
	var exists int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM residents WHERE code = ?", code).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return mergeCellError(sheet, row, "编号", fmt.Sprintf("居民编号 %s 不存在于数据库 Sheet", code))
	}
	if err != nil {
		return err
	}
	return nil
}

func parseMergeResidentKind(sheet string, row XLSXRow) (string, error) {
	switch textCell(row, "常驻居民/城邦居民") {
	case "常驻居民":
		return KindNPC, nil
	case "城邦居民":
		return KindPlayer, nil
	default:
		return "", mergeCellError(sheet, row.Index, "常驻居民/城邦居民", "必须是 常驻居民 或 城邦居民")
	}
}

func parseMergeGoldType(sheet string, row XLSXRow) (string, error) {
	switch textCell(row, "类型") {
	case "存入":
		return GoldIn, nil
	case "取出":
		return GoldOut, nil
	case "罚没":
		return GoldForfeit, nil
	case "拨付":
		return GoldAllocate, nil
	default:
		return "", mergeCellError(sheet, row.Index, "类型", "必须是 存入/取出/罚没/拨付")
	}
}

func parseMergeVoided(sheet string, row XLSXRow) (bool, error) {
	switch textCell(row, "状态") {
	case "", "有效":
		return false, nil
	case "已作废":
		return true, nil
	default:
		return false, mergeCellError(sheet, row.Index, "状态", "必须是 有效 或 已作废")
	}
}

func parseMergeDeletedAt(s *Store, sheet string, row XLSXRow, column string, occurredAt string) (string, error) {
	switch textCell(row, column) {
	case "", "有效":
		return "", nil
	case "已删除":
		return occurredAt, nil
	default:
		return "", mergeCellError(sheet, row.Index, column, "必须是 有效 或 已删除")
	}
}

func parseMergeInt(sheet string, row XLSXRow, column string) (int64, error) {
	raw := textCell(row, column)
	if raw == "" {
		return 0, mergeCellError(sheet, row.Index, column, "不能为空")
	}
	if value, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return value, nil
	}
	floatValue, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.Trunc(floatValue) != floatValue {
		return 0, mergeCellError(sheet, row.Index, column, fmt.Sprintf("必须是整数，当前为 %q", raw))
	}
	return int64(floatValue), nil
}

func parseMergeTime(s *Store, sheet string, row XLSXRow, column string, required bool) (string, error) {
	raw := textCell(row, column)
	if raw == "" {
		if required {
			return "", mergeCellError(sheet, row.Index, column, "不能为空")
		}
		return "", nil
	}
	t, err := parseDBTime(raw, s.loc)
	if err != nil {
		return "", mergeCellError(sheet, row.Index, column, err.Error())
	}
	return t.In(s.loc).Format(time.RFC3339), nil
}

func minutesBetween(loc *time.Location, enterRaw string, leaveRaw string) (int, error) {
	enterAt, err := parseDBTime(enterRaw, loc)
	if err != nil {
		return 0, err
	}
	leaveAt, err := parseDBTime(leaveRaw, loc)
	if err != nil {
		return 0, err
	}
	minutes := int(math.Round(leaveAt.Sub(enterAt).Minutes()))
	if minutes <= 0 {
		return 0, fmt.Errorf("离城时间必须晚于进城时间")
	}
	return minutes, nil
}

func parseInlineExtension(s *Store, sheet string, rowIndex int, raw string) (string, int, error) {
	if raw == "" {
		return "", 0, mergeCellError(sheet, rowIndex, "时长增加记录", "存在空记录")
	}
	plus := strings.LastIndex(raw, "+")
	if plus < 0 {
		return "", 0, mergeCellError(sheet, rowIndex, "时长增加记录", fmt.Sprintf("无法解析 %q", raw))
	}
	timeText := strings.TrimSpace(raw[:plus])
	minuteText := strings.TrimSpace(strings.TrimSuffix(raw[plus+1:], "分钟"))
	minutes64, err := strconv.ParseInt(minuteText, 10, 32)
	if err != nil || minutes64 <= 0 {
		return "", 0, mergeCellError(sheet, rowIndex, "时长增加记录", fmt.Sprintf("无法解析分钟数 %q", raw))
	}
	t, err := parseDBTime(timeText, s.loc)
	if err != nil {
		return "", 0, mergeCellError(sheet, rowIndex, "时长增加记录", err.Error())
	}
	return t.In(s.loc).Format(time.RFC3339), int(minutes64), nil
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
