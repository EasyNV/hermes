package importer

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"strings"
)

// ParsedRow represents a single parsed row from a CSV import.
type ParsedRow struct {
	RowNum       int
	Phone        string
	Name         string
	Tags         []string
	CustomFields map[string]string
	Err          string // non-empty if this row failed validation
}

// ParseCSV streams through CSV data row by row, applying column mapping and
// default tags. It calls fn for each parsed row (including failed ones).
//
// columnMapping maps Hermes field names to CSV column headers.
// Required key: "phone". Optional: "name", "tags" (comma-delimited in CSV).
// Any other key is treated as a custom field name mapped to a CSV column.
func ParseCSV(data []byte, columnMapping map[string]string, defaultTags []string, fn func(ParsedRow)) error {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.TrimLeadingSpace = true
	reader.LazyQuotes = true

	// Read header row.
	headers, err := reader.Read()
	if err != nil {
		return fmt.Errorf("reading CSV header: %w", err)
	}

	// Build header name → column index lookup.
	headerIndex := make(map[string]int, len(headers))
	for i, h := range headers {
		headerIndex[strings.TrimSpace(h)] = i
	}

	// Resolve column mapping: hermes field name → column index.
	fieldIndex := make(map[string]int, len(columnMapping))
	for hermesField, csvHeader := range columnMapping {
		idx, ok := headerIndex[csvHeader]
		if !ok {
			return fmt.Errorf("CSV column %q (mapped from %q) not found in headers", csvHeader, hermesField)
		}
		fieldIndex[hermesField] = idx
	}

	if _, ok := fieldIndex["phone"]; !ok {
		return fmt.Errorf("column mapping must include 'phone'")
	}

	rowNum := 0
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		rowNum++
		if err != nil {
			fn(ParsedRow{RowNum: rowNum, Err: fmt.Sprintf("CSV parse error: %v", err)})
			continue
		}

		row := ParsedRow{
			RowNum:       rowNum,
			CustomFields: make(map[string]string),
		}

		// Phone (required).
		if idx, ok := fieldIndex["phone"]; ok && idx < len(record) {
			row.Phone = strings.TrimSpace(record[idx])
		}
		if row.Phone == "" {
			row.Err = "missing required field: phone"
			fn(row)
			continue
		}

		// Name (optional).
		if idx, ok := fieldIndex["name"]; ok && idx < len(record) {
			row.Name = strings.TrimSpace(record[idx])
		}

		// Tags: comma-delimited in a single CSV column, plus default tags.
		var rowTags []string
		if idx, ok := fieldIndex["tags"]; ok && idx < len(record) {
			raw := strings.TrimSpace(record[idx])
			if raw != "" {
				for _, t := range strings.Split(raw, ",") {
					if t = strings.TrimSpace(t); t != "" {
						rowTags = append(rowTags, t)
					}
				}
			}
		}
		row.Tags = dedupStrings(append(rowTags, defaultTags...))

		// Custom fields: any mapping key that isn't phone/name/tags.
		for hermesField, colIdx := range fieldIndex {
			switch hermesField {
			case "phone", "name", "tags":
				continue
			}
			if colIdx < len(record) {
				if val := strings.TrimSpace(record[colIdx]); val != "" {
					row.CustomFields[hermesField] = val
				}
			}
		}

		fn(row)
	}

	return nil
}

func dedupStrings(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ss))
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}
