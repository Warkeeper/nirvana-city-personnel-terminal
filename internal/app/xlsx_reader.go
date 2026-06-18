package app

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
)

type XLSXWorkbook struct {
	Sheets map[string]*XLSXSheet
}

type XLSXSheet struct {
	Name    string
	Headers []string
	Rows    []XLSXRow
}

type XLSXRow struct {
	Index int
	Cells map[string]XLSXCell
}

type XLSXCell struct {
	Ref   string
	Type  string
	Value string
}

func ReadXLSX(filePath string) (*XLSXWorkbook, error) {
	reader, err := zip.OpenReader(filePath)
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	files := map[string]*zip.File{}
	for _, file := range reader.File {
		files[file.Name] = file
	}
	readFile := func(name string) ([]byte, error) {
		file := files[name]
		if file == nil {
			return nil, fmt.Errorf("xlsx missing %s", name)
		}
		rc, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}

	shared, err := readSharedStrings(readOptionalFile(files, "xl/sharedStrings.xml"))
	if err != nil {
		return nil, err
	}
	relsBytes, err := readFile("xl/_rels/workbook.xml.rels")
	if err != nil {
		return nil, err
	}
	rels, err := parseWorkbookRels(relsBytes)
	if err != nil {
		return nil, err
	}
	workbookBytes, err := readFile("xl/workbook.xml")
	if err != nil {
		return nil, err
	}
	sheetRefs, err := parseWorkbookSheets(workbookBytes)
	if err != nil {
		return nil, err
	}
	out := &XLSXWorkbook{Sheets: map[string]*XLSXSheet{}}
	for _, ref := range sheetRefs {
		target := rels[ref.RelID]
		if target == "" {
			return nil, fmt.Errorf("xlsx workbook sheet %q has missing relationship %q", ref.Name, ref.RelID)
		}
		sheetPath := path.Clean(path.Join("xl", target))
		if strings.HasPrefix(target, "/") {
			sheetPath = strings.TrimPrefix(path.Clean(target), "/")
		}
		bytes, err := readFile(sheetPath)
		if err != nil {
			return nil, err
		}
		sheet, err := parseWorksheet(ref.Name, bytes, shared)
		if err != nil {
			return nil, err
		}
		out.Sheets[sheet.Name] = sheet
	}
	return out, nil
}

func readOptionalFile(files map[string]*zip.File, name string) []byte {
	file := files[name]
	if file == nil {
		return nil
	}
	rc, err := file.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()
	bytes, _ := io.ReadAll(rc)
	return bytes
}

type workbookSheetRef struct {
	Name  string
	RelID string
}

func parseWorkbookSheets(data []byte) ([]workbookSheetRef, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	var refs []workbookSheetRef
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return refs, nil
		}
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "sheet" {
			continue
		}
		var ref workbookSheetRef
		for _, attr := range start.Attr {
			switch attr.Name.Local {
			case "name":
				ref.Name = attr.Value
			case "id":
				ref.RelID = attr.Value
			}
		}
		if ref.Name != "" && ref.RelID != "" {
			refs = append(refs, ref)
		}
	}
}

func parseWorkbookRels(data []byte) (map[string]string, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	rels := map[string]string{}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return rels, nil
		}
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "Relationship" {
			continue
		}
		var id, target string
		for _, attr := range start.Attr {
			switch attr.Name.Local {
			case "Id":
				id = attr.Value
			case "Target":
				target = attr.Value
			}
		}
		if id != "" && target != "" {
			rels[id] = target
		}
	}
}

func readSharedStrings(data []byte) ([]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	var out []string
	var inSI bool
	var inText bool
	var b strings.Builder
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Local == "si" {
				inSI = true
				b.Reset()
			}
			if inSI && t.Name.Local == "t" {
				inText = true
			}
		case xml.CharData:
			if inSI && inText {
				b.Write([]byte(t))
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				inText = false
			}
			if t.Name.Local == "si" {
				out = append(out, b.String())
				inSI = false
			}
		}
	}
}

func parseWorksheet(name string, data []byte, shared []string) (*XLSXSheet, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	var parsedRows []parsedXLSXRow
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "row" {
			continue
		}
		row, err := parseWorksheetRow(decoder, start, shared)
		if err != nil {
			return nil, fmt.Errorf("sheet %s: %w", name, err)
		}
		if len(row.Cells) > 0 {
			parsedRows = append(parsedRows, row)
		}
	}
	if len(parsedRows) == 0 {
		return &XLSXSheet{Name: name}, nil
	}
	headerByCol := map[int]string{}
	headers := []string{}
	for _, cell := range parsedRows[0].Cells {
		header := strings.TrimSpace(cell.Value)
		if header == "" {
			continue
		}
		col, _, err := splitCellRef(cell.Ref)
		if err != nil {
			return nil, fmt.Errorf("sheet %s header cell %s: %w", name, cell.Ref, err)
		}
		headerByCol[col] = header
		headers = append(headers, header)
	}
	sheet := &XLSXSheet{Name: name, Headers: headers}
	for _, parsed := range parsedRows[1:] {
		row := XLSXRow{Index: parsed.Index, Cells: map[string]XLSXCell{}}
		for _, cell := range parsed.Cells {
			col, _, err := splitCellRef(cell.Ref)
			if err != nil {
				return nil, fmt.Errorf("sheet %s row %d cell %s: %w", name, parsed.Index, cell.Ref, err)
			}
			header := headerByCol[col]
			if header == "" {
				continue
			}
			row.Cells[header] = cell
		}
		sheet.Rows = append(sheet.Rows, row)
	}
	return sheet, nil
}

type parsedXLSXRow struct {
	Index int
	Cells []XLSXCell
}

func parseWorksheetRow(decoder *xml.Decoder, start xml.StartElement, shared []string) (parsedXLSXRow, error) {
	row := parsedXLSXRow{}
	for _, attr := range start.Attr {
		if attr.Name.Local == "r" {
			row.Index, _ = strconv.Atoi(attr.Value)
		}
	}
	for {
		token, err := decoder.Token()
		if err != nil {
			return row, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Local == "c" {
				cell, err := parseWorksheetCell(decoder, t, shared)
				if err != nil {
					return row, err
				}
				if strings.TrimSpace(cell.Value) != "" {
					row.Cells = append(row.Cells, cell)
				}
			}
		case xml.EndElement:
			if t.Name.Local == "row" {
				if row.Index == 0 && len(row.Cells) > 0 {
					_, row.Index, _ = splitCellRef(row.Cells[0].Ref)
				}
				return row, nil
			}
		}
	}
}

func parseWorksheetCell(decoder *xml.Decoder, start xml.StartElement, shared []string) (XLSXCell, error) {
	cell := XLSXCell{}
	for _, attr := range start.Attr {
		switch attr.Name.Local {
		case "r":
			cell.Ref = attr.Value
		case "t":
			cell.Type = attr.Value
		}
	}
	var inV bool
	var inText bool
	var v strings.Builder
	var text strings.Builder
	for {
		token, err := decoder.Token()
		if err != nil {
			return cell, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Local == "v" {
				inV = true
			}
			if t.Name.Local == "t" {
				inText = true
			}
		case xml.CharData:
			if inV {
				v.Write([]byte(t))
			}
			if inText {
				text.Write([]byte(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v":
				inV = false
			case "t":
				inText = false
			case "c":
				raw := strings.TrimSpace(v.String())
				switch cell.Type {
				case "s":
					index, err := strconv.Atoi(raw)
					if err != nil {
						return cell, fmt.Errorf("shared string index %q is invalid", raw)
					}
					if index < 0 || index >= len(shared) {
						return cell, fmt.Errorf("shared string index %d out of range", index)
					}
					cell.Value = shared[index]
				case "inlineStr":
					cell.Value = text.String()
				default:
					if text.Len() > 0 {
						cell.Value = text.String()
					} else {
						cell.Value = raw
					}
				}
				return cell, nil
			}
		}
	}
}

func splitCellRef(ref string) (int, int, error) {
	if ref == "" {
		return 0, 0, fmt.Errorf("empty cell ref")
	}
	var col int
	var i int
	for ; i < len(ref); i++ {
		ch := ref[i]
		if ch >= 'a' && ch <= 'z' {
			ch = ch - 'a' + 'A'
		}
		if ch < 'A' || ch > 'Z' {
			break
		}
		col = col*26 + int(ch-'A'+1)
	}
	if col == 0 || i == len(ref) {
		return 0, 0, fmt.Errorf("invalid cell ref %q", ref)
	}
	row, err := strconv.Atoi(ref[i:])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid cell ref %q", ref)
	}
	return col, row, nil
}

func requireSheets(book *XLSXWorkbook, names []string) error {
	var missing []string
	for _, name := range names {
		if book.Sheets[name] == nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("unsupported workbook format: missing sheet(s): %s", strings.Join(missing, ", "))
	}
	return nil
}

func requireHeaders(sheet *XLSXSheet, headers []string) error {
	present := map[string]bool{}
	for _, header := range sheet.Headers {
		present[header] = true
	}
	var missing []string
	for _, header := range headers {
		if !present[header] {
			missing = append(missing, header)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("sheet %s missing column(s): %s", sheet.Name, strings.Join(missing, ", "))
	}
	return nil
}

func textCell(row XLSXRow, column string) string {
	return strings.TrimSpace(row.Cells[column].Value)
}

func requireTextCodeCell(sheet string, row XLSXRow, column string) (string, error) {
	cell := row.Cells[column]
	value := strings.TrimSpace(cell.Value)
	if value == "" {
		return "", mergeCellError(sheet, row.Index, column, "编号不能为空")
	}
	switch cell.Type {
	case "s", "str", "inlineStr":
		return value, nil
	default:
		return "", mergeCellError(sheet, row.Index, column, fmt.Sprintf("编号单元格必须是文本，当前类型为 %q", cell.Type))
	}
}

func mergeCellError(sheet string, row int, column string, message string) error {
	return fmt.Errorf("%s row %d column %s: %s", sheet, row, column, message)
}
