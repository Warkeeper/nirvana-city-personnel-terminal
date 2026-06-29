package app

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

func (s *Store) ExportData(ctx context.Context) (*ExportData, error) {
	residents, err := s.loadResidents(ctx)
	if err != nil {
		return nil, err
	}
	_, historyTexts, err := s.loadIdentityHistory(ctx)
	if err != nil {
		return nil, err
	}
	data := &ExportData{}
	data.Sheets = append(data.Sheets, s.databaseSheet(residents, historyTexts))
	goldSheet, err := s.goldSheet(ctx)
	if err != nil {
		return nil, err
	}
	data.Sheets = append(data.Sheets, goldSheet)
	travelSheet, canceledSheet, err := s.travelSheets(ctx)
	if err != nil {
		return nil, err
	}
	data.Sheets = append(data.Sheets, travelSheet)
	identitySheet, err := s.identitySheet(ctx)
	if err != nil {
		return nil, err
	}
	data.Sheets = append(data.Sheets, identitySheet)
	extensionSheet, err := s.extensionSheet(ctx)
	if err != nil {
		return nil, err
	}
	data.Sheets = append(data.Sheets, extensionSheet)
	sessionSheet, err := s.sessionSheet(ctx)
	if err != nil {
		return nil, err
	}
	data.Sheets = append(data.Sheets, sessionSheet)
	data.Sheets = append(data.Sheets, canceledSheet)
	return data, nil
}

func (s *Store) databaseSheet(residents []residentRow, historyTexts map[string][]string) Sheet {
	sheet := Sheet{
		Name:    "数据库",
		Columns: []string{"姓名", "编号", "金条余额", "常驻居民/城邦居民", "当前身份", "历史身份记录", "备注"},
	}
	for _, resident := range residents {
		kind := "城邦居民"
		if resident.Kind == KindNPC {
			kind = "常驻居民"
		}
		sheet.Rows = append(sheet.Rows, map[string]string{
			"姓名":        resident.Name,
			"编号":        resident.Code,
			"金条余额":      strconv.FormatInt(resident.Balance, 10),
			"常驻居民/城邦居民": kind,
			"当前身份":      resident.Identity,
			"历史身份记录":    strings.Join(historyTexts[resident.Code], ","),
			"备注":        resident.Remark,
		})
	}
	return sheet
}

func (s *Store) goldSheet(ctx context.Context) (Sheet, error) {
	sheet := Sheet{
		Name:    "金条流水",
		Columns: []string{"时间", "编号", "姓名", "当前身份", "类型", "数量", "操作后余额", "备注", "状态", "操作员"},
	}
	rows, err := s.db.QueryContext(ctx, `SELECT occurred_at, resident_code, resident_name_snapshot, identity_snapshot, record_type,
		amount, balance_after, remark, voided, operator FROM gold_records ORDER BY occurred_at ASC, id ASC`)
	if err != nil {
		return sheet, err
	}
	defer rows.Close()
	for rows.Next() {
		var occurred, code, name, identity, typ, remark, operator string
		var amount, balance int64
		var voided int
		if err := rows.Scan(&occurred, &code, &name, &identity, &typ, &amount, &balance, &remark, &voided, &operator); err != nil {
			return sheet, err
		}
		status := "有效"
		if voided != 0 {
			status = "已作废"
		}
		sheet.Rows = append(sheet.Rows, map[string]string{
			"时间":    s.formatDisplayTime(occurred),
			"编号":    code,
			"姓名":    name,
			"当前身份":  identity,
			"类型":    goldTypeLabel(typ),
			"数量":    strconv.FormatInt(amount, 10),
			"操作后余额": strconv.FormatInt(balance, 10),
			"备注":    remark,
			"状态":    status,
			"操作员":   operator,
		})
	}
	return sheet, rows.Err()
}

func (s *Store) travelSheets(ctx context.Context) (Sheet, Sheet, error) {
	active := Sheet{
		Name:    "玩家进出城记录",
		Columns: []string{"开城时间", "姓名", "编号", "当前身份", "进城时间", "离城时间", "时长增加记录", "操作员"},
	}
	canceled := Sheet{
		Name:    "已取消进出城记录",
		Columns: []string{"开城时间", "姓名", "编号", "当前身份", "进城时间", "离城时间", "取消时间", "操作员"},
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_opened_at, resident_code, resident_name_snapshot, identity_snapshot,
		enter_at, leave_at, canceled_at, operator FROM travel_records ORDER BY enter_at ASC, id ASC`)
	if err != nil {
		return active, canceled, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var sessionOpenedAt, code, name, identity, enterAt, leaveAt, operator string
		var canceledAt sql.NullString
		if err := rows.Scan(&id, &sessionOpenedAt, &code, &name, &identity, &enterAt, &leaveAt, &canceledAt, &operator); err != nil {
			return active, canceled, err
		}
		if canceledAt.Valid {
			canceled.Rows = append(canceled.Rows, map[string]string{
				"开城时间": s.formatDisplayTime(sessionOpenedAt),
				"姓名":   name,
				"编号":   code,
				"当前身份": identity,
				"进城时间": s.formatDisplayTime(enterAt),
				"离城时间": s.formatDisplayTime(leaveAt),
				"取消时间": s.formatDisplayTime(canceledAt.String),
				"操作员":  operator,
			})
			continue
		}
		extensions, err := s.extensionDisplay(ctx, id)
		if err != nil {
			return active, canceled, err
		}
		active.Rows = append(active.Rows, map[string]string{
			"开城时间":   s.formatDisplayTime(sessionOpenedAt),
			"姓名":     name,
			"编号":     code,
			"当前身份":   identity,
			"进城时间":   s.formatDisplayTime(enterAt),
			"离城时间":   s.formatDisplayTime(leaveAt),
			"时长增加记录": strings.Join(extensions, ","),
			"操作员":    operator,
		})
	}
	return active, canceled, rows.Err()
}

func (s *Store) identitySheet(ctx context.Context) (Sheet, error) {
	sheet := Sheet{
		Name:    "身份历史",
		Columns: []string{"时间", "编号", "姓名快照", "身份", "状态"},
	}
	rows, err := s.db.QueryContext(ctx, `SELECT occurred_at, resident_code, resident_name_snapshot, identity, deleted_at
		FROM identity_history ORDER BY occurred_at ASC, id ASC`)
	if err != nil {
		return sheet, err
	}
	defer rows.Close()
	for rows.Next() {
		var occurred, code, name, identity string
		var deleted sql.NullString
		if err := rows.Scan(&occurred, &code, &name, &identity, &deleted); err != nil {
			return sheet, err
		}
		status := "有效"
		if deleted.Valid {
			status = "已删除"
		}
		sheet.Rows = append(sheet.Rows, map[string]string{
			"时间":   s.formatDisplayTime(occurred),
			"编号":   code,
			"姓名快照": name,
			"身份":   identity,
			"状态":   status,
		})
	}
	return sheet, rows.Err()
}

func (s *Store) extensionSheet(ctx context.Context) (Sheet, error) {
	sheet := Sheet{
		Name:    "时长增加记录",
		Columns: []string{"进出城记录ID", "编号", "姓名", "增加分钟", "操作时间", "操作员"},
	}
	rows, err := s.db.QueryContext(ctx, `SELECT e.travel_id, t.resident_code, t.resident_name_snapshot, e.added_minutes, e.occurred_at, e.operator
		FROM travel_extensions e
		JOIN travel_records t ON t.id = e.travel_id
		ORDER BY e.occurred_at ASC, e.id ASC`)
	if err != nil {
		return sheet, err
	}
	defer rows.Close()
	for rows.Next() {
		var travelID int64
		var code, name, occurred, operator string
		var minutes int
		if err := rows.Scan(&travelID, &code, &name, &minutes, &occurred, &operator); err != nil {
			return sheet, err
		}
		sheet.Rows = append(sheet.Rows, map[string]string{
			"进出城记录ID": strconv.FormatInt(travelID, 10),
			"编号":      code,
			"姓名":      name,
			"增加分钟":    strconv.Itoa(minutes),
			"操作时间":    s.formatDisplayTime(occurred),
			"操作员":     operator,
		})
	}
	return sheet, rows.Err()
}

func (s *Store) sessionSheet(ctx context.Context) (Sheet, error) {
	sheet := Sheet{
		Name:    "开城记录",
		Columns: []string{"开城时间", "关城时间", "操作员", "备注"},
	}
	rows, err := s.db.QueryContext(ctx, "SELECT opened_at, closed_at, operator, note FROM city_sessions ORDER BY opened_at ASC")
	if err != nil {
		return sheet, err
	}
	defer rows.Close()
	for rows.Next() {
		var opened, operator, note string
		var closed sql.NullString
		if err := rows.Scan(&opened, &closed, &operator, &note); err != nil {
			return sheet, err
		}
		closedText := ""
		if closed.Valid {
			closedText = s.formatDisplayTime(closed.String)
		}
		sheet.Rows = append(sheet.Rows, map[string]string{
			"开城时间": s.formatDisplayTime(opened),
			"关城时间": closedText,
			"操作员":  operator,
			"备注":   note,
		})
	}
	return sheet, rows.Err()
}

func (s *Store) extensionDisplay(ctx context.Context, travelID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT added_minutes, occurred_at FROM travel_extensions WHERE travel_id = ? ORDER BY occurred_at ASC, id ASC", travelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var minutes int
		var occurred string
		if err := rows.Scan(&minutes, &occurred); err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%s +%d分钟", s.formatDisplayTime(occurred), minutes))
	}
	return out, rows.Err()
}

func XLSXBytes(data *ExportData) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := map[string]string{
		"[Content_Types].xml":        contentTypesXML(len(data.Sheets)),
		"_rels/.rels":                relsXML(),
		"xl/workbook.xml":            workbookXML(data.Sheets),
		"xl/_rels/workbook.xml.rels": workbookRelsXML(len(data.Sheets)),
		"xl/styles.xml":              stylesXML(),
		"docProps/core.xml":          coreXML(),
		"docProps/app.xml":           appXML(),
	}
	for name, body := range files {
		if err := writeZipText(zw, name, body); err != nil {
			return nil, err
		}
	}
	for i, sheet := range data.Sheets {
		if err := writeZipText(zw, fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1), sheetXML(sheet)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeZipText(zw *zip.Writer, name, body string) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, body)
	return err
}

func contentTypesXML(sheetCount int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`)
	b.WriteString(`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>`)
	b.WriteString(`<Default Extension="xml" ContentType="application/xml"/>`)
	b.WriteString(`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>`)
	b.WriteString(`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`)
	b.WriteString(`<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>`)
	b.WriteString(`<Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>`)
	for i := 1; i <= sheetCount; i++ {
		b.WriteString(fmt.Sprintf(`<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, i))
	}
	b.WriteString(`</Types>`)
	return b.String()
}

func relsXML() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>
<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/>
</Relationships>`
}

func workbookXML(sheets []Sheet) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	for i, sheet := range sheets {
		b.WriteString(fmt.Sprintf(`<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, xmlEscapeAttr(sheet.Name), i+1, i+1))
	}
	b.WriteString(`</sheets></workbook>`)
	return b.String()
}

func workbookRelsXML(sheetCount int) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for i := 1; i <= sheetCount; i++ {
		b.WriteString(fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, i, i))
	}
	b.WriteString(fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`, sheetCount+1))
	b.WriteString(`</Relationships>`)
	return b.String()
}

func stylesXML() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts>
<fills count="1"><fill><patternFill patternType="none"/></fill></fills>
<borders count="1"><border/></borders>
<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>
<cellXfs count="1"><xf numFmtId="49" fontId="0" fillId="0" borderId="0" xfId="0"/></cellXfs>
<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>
</styleSheet>`
}

func coreXML() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:dcmitype="http://purl.org/dc/dcmitype/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
<dc:creator>nirvana-city-personnel-terminal</dc:creator>
<cp:lastModifiedBy>nirvana-city-personnel-terminal</cp:lastModifiedBy>
</cp:coreProperties>`
}

func appXML() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes">
<Application>nirvana-city-personnel-terminal</Application>
</Properties>`
}

func sheetXML(sheet Sheet) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	rowIndex := 1
	b.WriteString(rowXML(rowIndex, sheet.Columns))
	rowIndex++
	for _, row := range sheet.Rows {
		values := make([]string, 0, len(sheet.Columns))
		for _, col := range sheet.Columns {
			values = append(values, row[col])
		}
		b.WriteString(rowXML(rowIndex, values))
		rowIndex++
	}
	b.WriteString(`</sheetData></worksheet>`)
	return b.String()
}

func rowXML(rowIndex int, values []string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf(`<row r="%d">`, rowIndex))
	for i, value := range values {
		cell := fmt.Sprintf("%s%d", columnName(i+1), rowIndex)
		b.WriteString(fmt.Sprintf(`<c r="%s" t="inlineStr" s="0"><is><t>%s</t></is></c>`, cell, xmlEscapeText(value)))
	}
	b.WriteString(`</row>`)
	return b.String()
}

func columnName(index int) string {
	var chars []byte
	for index > 0 {
		index--
		chars = append(chars, byte('A'+index%26))
		index /= 26
	}
	for i, j := 0, len(chars)-1; i < j; i, j = i+1, j-1 {
		chars[i], chars[j] = chars[j], chars[i]
	}
	return string(chars)
}

func xmlEscapeText(value string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}

func xmlEscapeAttr(value string) string {
	return strings.ReplaceAll(xmlEscapeText(value), `"`, "&quot;")
}

func sortSheetsByRequiredOrder(data *ExportData) {
	order := map[string]int{
		"数据库":      0,
		"金条流水":     1,
		"玩家进出城记录":  2,
		"身份历史":     3,
		"时长增加记录":   4,
		"开城记录":     5,
		"已取消进出城记录": 6,
	}
	sort.SliceStable(data.Sheets, func(i, j int) bool {
		return order[data.Sheets[i].Name] < order[data.Sheets[j].Name]
	})
}
