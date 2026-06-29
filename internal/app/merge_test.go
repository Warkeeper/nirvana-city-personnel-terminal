package app

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMergeRejectsOldThreeSheetWorkbook(t *testing.T) {
	store := newMergeStore(t)
	path := writeMergeWorkbook(t, &ExportData{Sheets: []Sheet{
		{Name: "数据库", Columns: []string{"姓名", "编号"}, Rows: []map[string]string{}},
		{Name: "金条流水", Columns: []string{"时间", "编号"}, Rows: []map[string]string{}},
		{Name: "玩家进出城记录", Columns: []string{"开城记录ID", "编号"}, Rows: []map[string]string{}},
	}})

	_, err := store.MergeWorkbook(context.Background(), path)
	if err == nil {
		t.Fatal("expected old workbook format to be rejected")
	}
	if !strings.Contains(err.Error(), "missing sheet") || !strings.Contains(err.Error(), "身份历史") {
		t.Fatalf("unexpected error: %v", err)
	}
	assertRowCount(t, store, "residents", 0)
}

func TestMergeRejectsLegacyOpenCityIDColumns(t *testing.T) {
	store := newMergeStore(t)
	data := mergeFixture(mergeFixtureOptions{})
	sheetByName(data, "玩家进出城记录").Columns[0] = "开城记录ID"
	path := writeMergeWorkbook(t, data)

	_, err := store.MergeWorkbook(context.Background(), path)
	if err == nil {
		t.Fatal("expected legacy open city id column to be rejected")
	}
	if !strings.Contains(err.Error(), "开城时间") {
		t.Fatalf("unexpected error: %v", err)
	}
	assertRowCount(t, store, "residents", 0)
}
func TestMergeWorkbookImportsUpdatesAndAvoidsDuplicates(t *testing.T) {
	store := newMergeStore(t)
	path := writeMergeWorkbook(t, mergeFixture(mergeFixtureOptions{}))

	report, err := store.MergeWorkbook(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if report.BackupPath == "" {
		t.Fatal("merge did not create a backup")
	}
	if _, err := os.Stat(report.BackupPath); err != nil {
		t.Fatalf("backup not found: %v", err)
	}
	assertMergeCounts(t, store, 2, 1, 2, 1, 1)
	assertResident(t, store, "01234", "零号", KindPlayer, -7, "城防部", "初始备注")
	assertExportRows(t, store, "玩家进出城记录", 1)
	assertExportRows(t, store, "已取消进出城记录", 1)

	if _, err := store.MergeWorkbook(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	assertMergeCounts(t, store, 2, 1, 2, 1, 1)

	updated := writeMergeWorkbook(t, mergeFixture(mergeFixtureOptions{
		residentName: "零号改名",
		balance:      -11,
		goldName:     "流水快照改",
		goldBalance:  -9,
		goldRemark:   "Excel 覆盖备注",
		goldStatus:   "已作废",
		sessionNote:  "Excel 覆盖开城备注",
	}))
	if _, err := store.MergeWorkbook(context.Background(), updated); err != nil {
		t.Fatal(err)
	}
	assertMergeCounts(t, store, 2, 1, 2, 1, 1)
	assertResident(t, store, "01234", "零号改名", KindPlayer, -11, "城防部", "初始备注")
	assertGoldRecord(t, store, "01234", "流水快照改", -9, "Excel 覆盖备注", true, true)
	assertSessionNote(t, store, "2026-06-18T09:00:00+08:00", "merge-op", "Excel 覆盖开城备注")
}

func TestMergeRejectsNumericResidentCodeAndRollsBack(t *testing.T) {
	store := newMergeStore(t)
	xlsx, err := XLSXBytes(mergeFixture(mergeFixtureOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	xlsx = replaceZipText(t, xlsx, "xl/worksheets/sheet1.xml", func(body string) string {
		old := `<c r="B2" t="inlineStr" s="0"><is><t>01234</t></is></c>`
		if !strings.Contains(body, old) {
			t.Fatalf("test workbook did not contain expected code cell")
		}
		return strings.Replace(body, old, `<c r="B2"><v>1234</v></c>`, 1)
	})
	path := writeBytes(t, xlsx, "numeric-code.xlsx")

	_, err = store.MergeWorkbook(context.Background(), path)
	if err == nil {
		t.Fatal("expected numeric code cell to be rejected")
	}
	if !strings.Contains(err.Error(), "数据库 row 2 column 编号") || !strings.Contains(err.Error(), "必须是文本") {
		t.Fatalf("unexpected error: %v", err)
	}
	assertRowCount(t, store, "residents", 0)
	assertRowCount(t, store, "gold_records", 0)
}

func TestMergeRollsBackOnResidentTypeConflict(t *testing.T) {
	store := newMergeStore(t)
	path := writeMergeWorkbook(t, mergeWorkbookWithResidentRows([]map[string]string{
		{"姓名": "先插入", "编号": "C-001", "金条余额": "1", "常驻居民/城邦居民": "城邦居民", "当前身份": "身份一", "历史身份记录": "", "备注": ""},
		{"姓名": "类型冲突", "编号": "C-001", "金条余额": "2", "常驻居民/城邦居民": "常驻居民", "当前身份": "身份二", "历史身份记录": "", "备注": ""},
	}))

	_, err := store.MergeWorkbook(context.Background(), path)
	if err == nil {
		t.Fatal("expected type conflict")
	}
	if !strings.Contains(err.Error(), "不能合并") {
		t.Fatalf("unexpected error: %v", err)
	}
	assertRowCount(t, store, "residents", 0)
}

func TestMergeBackupFailureBlocksImport(t *testing.T) {
	dataDir := t.TempDir()
	store, err := OpenStore(context.Background(), Config{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := os.WriteFile(filepath.Join(dataDir, "backups"), []byte("not a directory"), 0644); err != nil {
		t.Fatal(err)
	}
	path := writeMergeWorkbook(t, mergeFixture(mergeFixtureOptions{}))

	_, err = store.MergeWorkbook(context.Background(), path)
	if err == nil {
		t.Fatal("expected backup failure")
	}
	if !strings.Contains(err.Error(), "merge backup failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	assertRowCount(t, store, "residents", 0)
}

type mergeFixtureOptions struct {
	residentName string
	balance      int64
	goldName     string
	goldBalance  int64
	goldRemark   string
	goldStatus   string
	sessionNote  string
}

func mergeFixture(opts mergeFixtureOptions) *ExportData {
	if opts.residentName == "" {
		opts.residentName = "零号"
	}
	if opts.balance == 0 {
		opts.balance = -7
	}
	if opts.goldName == "" {
		opts.goldName = opts.residentName
	}
	if opts.goldBalance == 0 {
		opts.goldBalance = -3
	}
	if opts.goldRemark == "" {
		opts.goldRemark = "初始流水"
	}
	if opts.goldStatus == "" {
		opts.goldStatus = "有效"
	}
	if opts.sessionNote == "" {
		opts.sessionNote = "初始开城备注"
	}
	data := mergeWorkbookWithResidentRows([]map[string]string{
		{"姓名": opts.residentName, "编号": "01234", "金条余额": intText(opts.balance), "常驻居民/城邦居民": "城邦居民", "当前身份": "城防部", "历史身份记录": "", "备注": "初始备注"},
		{"姓名": "常驻", "编号": "NPC001", "金条余额": "5", "常驻居民/城邦居民": "常驻居民", "当前身份": "保安部", "历史身份记录": "", "备注": ""},
	})
	sheetByName(data, "金条流水").Rows = []map[string]string{{
		"时间": "2026/6/18 10:10:00", "编号": "01234", "姓名": opts.goldName, "当前身份": "城防部", "类型": "取出", "数量": "3",
		"操作后余额": intText(opts.goldBalance), "备注": opts.goldRemark, "状态": opts.goldStatus, "操作员": "merge-op",
	}}
	sheetByName(data, "玩家进出城记录").Rows = []map[string]string{{
		"开城时间": "2026/6/18 09:00:00", "姓名": opts.residentName, "编号": "01234", "当前身份": "城防部",
		"进城时间": "2026/6/18 10:00:00", "离城时间": "2026/6/18 12:30:00",
		"时长增加记录": "2026/6/18 11:00:00 +30分钟", "操作员": "merge-op",
	}}
	sheetByName(data, "身份历史").Rows = []map[string]string{{
		"时间": "2026/6/18 10:05:00", "编号": "01234", "姓名快照": "旧名", "身份": "城防部", "状态": "有效",
	}}
	sheetByName(data, "时长增加记录").Rows = []map[string]string{{
		"进出城记录ID": "200", "编号": "01234", "姓名": opts.residentName, "增加分钟": "30", "操作时间": "2026/6/18 11:00:00", "操作员": "merge-op",
	}}
	sheetByName(data, "开城记录").Rows = []map[string]string{{
		"开城时间": "2026/6/18 09:00:00", "关城时间": "2026/6/18 18:00:00", "操作员": "merge-op", "备注": opts.sessionNote,
	}}
	sheetByName(data, "已取消进出城记录").Rows = []map[string]string{{
		"开城时间": "2026/6/18 09:00:00", "姓名": "常驻", "编号": "NPC001", "当前身份": "保安部",
		"进城时间": "2026/6/18 13:00:00", "离城时间": "2026/6/18 14:00:00", "取消时间": "2026/6/18 13:10:00", "操作员": "merge-op",
	}}
	return data
}

func mergeWorkbookWithResidentRows(rows []map[string]string) *ExportData {
	return &ExportData{Sheets: []Sheet{
		{Name: "数据库", Columns: []string{"姓名", "编号", "金条余额", "常驻居民/城邦居民", "当前身份", "历史身份记录", "备注"}, Rows: rows},
		{Name: "金条流水", Columns: []string{"时间", "编号", "姓名", "当前身份", "类型", "数量", "操作后余额", "备注", "状态", "操作员"}, Rows: []map[string]string{}},
		{Name: "玩家进出城记录", Columns: []string{"开城时间", "姓名", "编号", "当前身份", "进城时间", "离城时间", "时长增加记录", "操作员"}, Rows: []map[string]string{}},
		{Name: "身份历史", Columns: []string{"时间", "编号", "姓名快照", "身份", "状态"}, Rows: []map[string]string{}},
		{Name: "时长增加记录", Columns: []string{"进出城记录ID", "编号", "姓名", "增加分钟", "操作时间", "操作员"}, Rows: []map[string]string{}},
		{Name: "开城记录", Columns: []string{"开城时间", "关城时间", "操作员", "备注"}, Rows: []map[string]string{}},
		{Name: "已取消进出城记录", Columns: []string{"开城时间", "姓名", "编号", "当前身份", "进城时间", "离城时间", "取消时间", "操作员"}, Rows: []map[string]string{}},
	}}
}

func newMergeStore(t *testing.T) *Store {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(context.Background(), Config{
		DataDir: t.TempDir(),
		Now: func() time.Time {
			return time.Date(2026, 6, 18, 16, 0, 0, 0, loc)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func sheetByName(data *ExportData, name string) *Sheet {
	for i := range data.Sheets {
		if data.Sheets[i].Name == name {
			return &data.Sheets[i]
		}
	}
	panic("missing sheet " + name)
}

func writeMergeWorkbook(t *testing.T, data *ExportData) string {
	t.Helper()
	xlsx, err := XLSXBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	return writeBytes(t, xlsx, "merge.xlsx")
}

func writeBytes(t *testing.T, data []byte, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func replaceZipText(t *testing.T, input []byte, name string, replace func(string) string) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(input), int64(len(input)))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for _, file := range reader.File {
		rc, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if file.Name == name {
			content = []byte(replace(string(content)))
		}
		w, err := writer.Create(file.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func assertMergeCounts(t *testing.T, store *Store, residents, gold, travel, identity, extensions int) {
	t.Helper()
	assertRowCount(t, store, "residents", residents)
	assertRowCount(t, store, "gold_records", gold)
	assertRowCount(t, store, "travel_records", travel)
	assertRowCount(t, store, "identity_history", identity)
	assertRowCount(t, store, "travel_extensions", extensions)
}

func assertRowCount(t *testing.T, store *Store, table string, want int) {
	t.Helper()
	var got int
	if err := store.db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s count=%d want %d", table, got, want)
	}
}

func assertResident(t *testing.T, store *Store, code, name, kind string, balance int64, identity, remark string) {
	t.Helper()
	var gotName, gotKind, gotIdentity, gotRemark string
	var gotBalance int64
	err := store.db.QueryRowContext(context.Background(), `SELECT name, kind, balance, identity_current, remark FROM residents WHERE code = ?`, code).
		Scan(&gotName, &gotKind, &gotBalance, &gotIdentity, &gotRemark)
	if err != nil {
		t.Fatal(err)
	}
	if gotName != name || gotKind != kind || gotBalance != balance || gotIdentity != identity || gotRemark != remark {
		t.Fatalf("resident mismatch: name=%q kind=%q balance=%d identity=%q remark=%q", gotName, gotKind, gotBalance, gotIdentity, gotRemark)
	}
}

func assertGoldRecord(t *testing.T, store *Store, code, name string, balance int64, remark string, voided bool, balanceReverted bool) {
	t.Helper()
	var gotName, gotRemark string
	var gotBalance int64
	var gotVoided, gotReverted int
	err := store.db.QueryRowContext(context.Background(), `SELECT resident_name_snapshot, balance_after, remark, voided, balance_reverted
		FROM gold_records WHERE resident_code = ?`, code).Scan(&gotName, &gotBalance, &gotRemark, &gotVoided, &gotReverted)
	if err != nil {
		t.Fatal(err)
	}
	if gotName != name || gotBalance != balance || gotRemark != remark || (gotVoided != 0) != voided || (gotReverted != 0) != balanceReverted {
		t.Fatalf("gold mismatch: name=%q balance=%d remark=%q voided=%d reverted=%d", gotName, gotBalance, gotRemark, gotVoided, gotReverted)
	}
}

func assertSessionNote(t *testing.T, store *Store, openedAt, operator, note string) {
	t.Helper()
	var got string
	err := store.db.QueryRowContext(context.Background(), `SELECT note FROM city_sessions WHERE opened_at = ? AND operator = ?`, openedAt, operator).Scan(&got)
	if err != nil {
		t.Fatal(err)
	}
	if got != note {
		t.Fatalf("session note=%q want %q", got, note)
	}
}

func assertExportRows(t *testing.T, store *Store, sheet string, want int) {
	t.Helper()
	exported, err := store.ExportData(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rows := sheetRows(t, exported, sheet)
	if len(rows) != want {
		t.Fatalf("%s export rows=%d want %d", sheet, len(rows), want)
	}
}

func intText(v int64) string {
	return strconv.FormatInt(v, 10)
}
