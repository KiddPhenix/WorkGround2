package control

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"path/filepath"
	"strconv"
	"strings"

	"workground2/internal/work"
)

const xlsxMediaType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

func materializeTaskArtifact(
	slot work.ArtifactSlot,
	content string,
) (body []byte, name, mediaType string, supported bool, err error) {
	kind := strings.ToLower(strings.TrimSpace(slot.Kind))
	switch kind {
	case "text", "txt", "plain_text", "text/plain":
		return []byte(content), artifactFileName(slot, ".txt"), "text/plain", true, nil
	case "markdown", "md", "document", "text/markdown":
		if strings.Contains(strings.ToLower(slot.ID), "txt") && kind == "document" {
			return []byte(content), artifactFileName(slot, ".txt"), "text/plain", true, nil
		}
		return []byte(content), artifactFileName(slot, ".md"), "text/markdown", true, nil
	case "xlsx", "spreadsheet", "excel":
		body, err := buildXLSX(content)
		return body, artifactFileName(slot, ".xlsx"), xlsxMediaType, true, err
	default:
		return nil, "", "", false, nil
	}
}

func artifactFileName(slot work.ArtifactSlot, extension string) string {
	base := strings.TrimSpace(slot.Title)
	if base == "" {
		base = strings.TrimSpace(slot.ID)
	}
	base = strings.Map(func(r rune) rune {
		switch r {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*':
			return '-'
		default:
			if r < 32 {
				return -1
			}
			return r
		}
	}, base)
	base = strings.Trim(strings.TrimSpace(base), ".")
	if base == "" {
		base = "artifact"
	}
	switch strings.ToLower(filepath.Ext(base)) {
	case ".md", ".markdown", ".txt", ".xlsx", ".xls", ".csv", ".json":
		base = strings.TrimSuffix(base, filepath.Ext(base))
	}
	return base + extension
}

func buildXLSX(content string) ([]byte, error) {
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	files := []struct {
		name string
		body string
	}{
		{
			name: "[Content_Types].xml",
			body: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
				`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
				`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
				`<Default Extension="xml" ContentType="application/xml"/>` +
				`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` +
				`<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>` +
				`</Types>`,
		},
		{
			name: "_rels/.rels",
			body: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
				`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
				`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>` +
				`</Relationships>`,
		},
		{
			name: "xl/workbook.xml",
			body: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
				`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
				`<sheets><sheet name="成果" sheetId="1" r:id="rId1"/></sheets>` +
				`</workbook>`,
		},
		{
			name: "xl/_rels/workbook.xml.rels",
			body: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
				`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
				`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>` +
				`</Relationships>`,
		},
		{name: "xl/worksheets/sheet1.xml", body: worksheetXML(spreadsheetRows(content))},
	}
	for _, file := range files {
		entry, err := writer.Create(file.name)
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write([]byte(file.body)); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func spreadsheetRows(content string) [][]string {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n"), "\n")
	var table [][]string
	inTable := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "|") {
			if inTable {
				break
			}
			continue
		}
		cells := splitMarkdownRow(trimmed)
		if len(cells) < 2 {
			if inTable {
				break
			}
			continue
		}
		inTable = true
		if !markdownSeparatorRow(cells) {
			table = append(table, cells)
		}
	}
	if len(table) > 0 {
		return table
	}
	rows := make([][]string, 0, len(lines))
	for _, line := range lines {
		if text := strings.TrimSpace(line); text != "" {
			rows = append(rows, []string{text})
		}
	}
	if len(rows) == 0 {
		return [][]string{{""}}
	}
	return rows
}

func splitMarkdownRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cells = append(cells, strings.TrimSpace(part))
	}
	return cells
}

func markdownSeparatorRow(cells []string) bool {
	for _, cell := range cells {
		cell = strings.TrimSpace(cell)
		cell = strings.Trim(cell, ":")
		if len(cell) < 3 || strings.Trim(cell, "-") != "" {
			return false
		}
	}
	return true
}

func worksheetXML(rows [][]string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for rowIndex, row := range rows {
		b.WriteString(`<row r="`)
		b.WriteString(strconv.Itoa(rowIndex + 1))
		b.WriteString(`">`)
		for columnIndex, value := range row {
			b.WriteString(`<c r="`)
			b.WriteString(xlsxColumn(columnIndex + 1))
			b.WriteString(strconv.Itoa(rowIndex + 1))
			b.WriteString(`" t="inlineStr"><is><t xml:space="preserve">`)
			var escaped bytes.Buffer
			if err := xml.EscapeText(&escaped, []byte(value)); err != nil {
				continue
			}
			b.Write(escaped.Bytes())
			b.WriteString(`</t></is></c>`)
		}
		b.WriteString(`</row>`)
	}
	b.WriteString(`</sheetData></worksheet>`)
	return b.String()
}

func xlsxColumn(index int) string {
	if index <= 0 {
		return "A"
	}
	var column string
	for index > 0 {
		index--
		column = string(rune('A'+index%26)) + column
		index /= 26
	}
	return column
}
