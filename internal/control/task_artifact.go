package control

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"workground2/internal/artifact"
	"workground2/internal/work"
)

const xlsxMediaType = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

func materializeTaskArtifact(
	slot work.ArtifactSlot,
	content string,
	discovered *artifact.Discovered,
	workspaceRoot string,
) (body []byte, name, mediaType string, supported bool, err error) {
	if discovered != nil {
		pathOnly := len(discovered.Data) == 0
		item, loadErr := artifact.LoadWorkspaceFile(*discovered, workspaceRoot)
		if loadErr != nil {
			return nil, "", "", false, fmt.Errorf("load artifact %q: %w", discovered.Name, loadErr)
		}
		if len(item.Data) == 0 {
			return nil, "", "", false, fmt.Errorf("artifact %q has no data", item.Name)
		}
		if pathOnly && strings.EqualFold(strings.TrimSpace(slot.Kind), "image") {
			mime, imageErr := artifact.ValidateImageData(item.Data)
			if imageErr != nil {
				return nil, "", "", false, fmt.Errorf("validate image artifact %q: %w", item.Name, imageErr)
			}
			item.Type = mime
		}
		return item.Data, item.Name, item.Type, true, nil
	}
	kind := strings.ToLower(strings.TrimSpace(slot.Kind))
	switch kind {
	case "text", "txt", "plain_text", "text/plain":
		if content == "" {
			return nil, "", "", false, nil
		}
		return []byte(content), artifactFileName(slot, ".txt"), "text/plain", true, nil
	case "code", "source", "source_code":
		if content == "" {
			return nil, "", "", false, nil
		}
		return []byte(content), artifactFileName(slot, ".txt"), "text/plain", true, nil
	case "markdown", "md", "document", "text/markdown":
		if content == "" {
			return nil, "", "", false, nil
		}
		if strings.Contains(strings.ToLower(slot.ID), "txt") && kind == "document" {
			return []byte(content), artifactFileName(slot, ".txt"), "text/plain", true, nil
		}
		return []byte(content), artifactFileName(slot, ".md"), "text/markdown", true, nil
	case "xlsx", "spreadsheet", "excel":
		if content == "" {
			return nil, "", "", false, nil
		}
		body, err := buildXLSX(content)
		return body, artifactFileName(slot, ".xlsx"), xlsxMediaType, true, err
	default:
		return nil, "", "", false, nil
	}
}

func taskArtifactRelativePath(discovered *artifact.Discovered, workspaceRoot string) string {
	if discovered == nil || strings.TrimSpace(discovered.Path) == "" || strings.TrimSpace(workspaceRoot) == "" {
		return ""
	}
	relative, err := filepath.Rel(workspaceRoot, discovered.Path)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative) {
		return ""
	}
	return filepath.ToSlash(relative)
}

func textArtifactKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "text", "txt", "plain_text", "text/plain",
		"code", "source", "source_code",
		"markdown", "md", "document", "text/markdown",
		"xlsx", "spreadsheet", "excel":
		return true
	default:
		return false
	}
}

// takeArtifacts deterministically assigns unconsumed artifacts by declared
// slot order and discovery order. A structured artifact can feed only one slot.
// Specific file formats are matched by SlotKind and extension aliases, while
// older broad document / file / archive declarations remain compatible.
func takeArtifacts(discovered []artifact.Discovered, used map[int]bool, wantKind string, count int) []int {
	want := strings.ToLower(strings.TrimSpace(wantKind))
	if want == "" || count <= 0 {
		return nil
	}
	indexes := make([]int, 0, count)
	for i := range discovered {
		if used[i] || !artifactKindMatches(discovered[i], want) {
			continue
		}
		indexes = append(indexes, i)
		if len(indexes) == count {
			break
		}
	}
	return indexes
}

func artifactKindMatches(item artifact.Discovered, wantKind string) bool {
	slotKind := strings.ToLower(strings.TrimSpace(item.SlotKind()))
	if slotKind == wantKind {
		return true
	}
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(item.Name)))
	switch wantKind {
	case "document":
		return ext == ".doc" || ext == ".docx" || ext == ".pdf"
	case "doc", "docx", "word":
		return ext == ".doc" || ext == ".docx"
	case "pdf":
		return ext == ".pdf"
	case "archive":
		return ext == ".zip" || ext == ".7z" || ext == ".gz" || ext == ".tar" ||
			strings.HasSuffix(strings.ToLower(item.Name), ".tar.gz") ||
			strings.HasSuffix(strings.ToLower(item.Name), ".tar.xz")
	case "zip":
		return ext == ".zip"
	case "file":
		// "file" is the broadest catch-all; anything that isn't media is a file.
		return slotKind != "image" && slotKind != "video" && slotKind != "audio"
	case "data":
		switch ext {
		case ".csv", ".json", ".tsv", ".xml", ".yaml", ".yml", ".xlsx", ".xls":
			return true
		}
	case "sh", "shell":
		return ext == ".sh"
	case "bat", "batch", "cmd":
		return ext == ".bat" || ext == ".cmd"
	case "ps1", "powershell":
		return ext == ".ps1"
	case "exe", "executable":
		switch ext {
		case ".exe", ".com", ".app", ".appimage":
			return true
		}
	}
	return false
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
	// Strip known compound extensions first (e.g. .tar.gz).
	for _, ce := range []string{".tar.gz", ".tar.xz", ".tar.bz2"} {
		if strings.HasSuffix(strings.ToLower(base), ce) {
			base = base[:len(base)-len(ce)]
			break
		}
	}
	switch strings.ToLower(filepath.Ext(base)) {
	case ".md", ".markdown", ".txt", ".xlsx", ".xls", ".csv", ".json",
		".docx", ".pdf", ".sh", ".bat", ".cmd", ".ps1", ".exe", ".zip", ".7z",
		".gz", ".tar":
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
