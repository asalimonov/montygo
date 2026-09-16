package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"github.com/asalimonov/montygo/monterr"
	"github.com/asalimonov/montygo/sandbox"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const tableName = "data"

var (
	integerPattern = regexp.MustCompile(`^[+-]?[0-9]+$`)
	realPattern    = regexp.MustCompile(`^[+-]?([0-9]+\.?[0-9]*|\.[0-9]+)([eE][+-]?[0-9]+)?$`)
)

// queryCSV loads content into an in-memory SQLite table named data and runs
// query with DuckDB-style $name parameters.
func queryCSV(ctx context.Context, content []byte, query string, parameters map[string]any) ([]any, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if err := loadCSV(ctx, db, content); err != nil {
		return nil, err
	}
	rewritten, args, err := bindParameters(query, parameters)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, rewritten, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []any{}
	for rows.Next() {
		values := make([]any, len(columns))
		ptrs := make([]any, len(columns))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := sandbox.NewDict()
		for i, column := range columns {
			row.Set(column, sqlValue(values[i]))
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func sqlValue(v any) any {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case time.Time:
		return x.Format(time.RFC3339Nano)
	}
	return v
}

type columnType int

const (
	columnInteger columnType = iota
	columnReal
	columnText
)

func (t columnType) sqlName() string {
	switch t {
	case columnInteger:
		return "INTEGER"
	case columnReal:
		return "REAL"
	}
	return "TEXT"
}

func inferColumnTypes(records [][]string, width int) []columnType {
	types := make([]columnType, width)
	seen := make([]bool, width)
	for _, record := range records {
		for i, field := range record {
			if field == "" {
				continue
			}
			seen[i] = true
			switch types[i] {
			case columnInteger:
				if _, err := strconv.ParseInt(field, 10, 64); err == nil && integerPattern.MatchString(field) {
					continue
				}
				types[i] = columnReal
				fallthrough
			case columnReal:
				if realPattern.MatchString(field) {
					continue
				}
				types[i] = columnText
			}
		}
	}
	for i := range types {
		if !seen[i] {
			types[i] = columnText
		}
	}
	return types
}

func columnNames(header []string) []string {
	names := make([]string, len(header))
	used := map[string]bool{}
	for i, name := range header {
		if name == "" {
			name = fmt.Sprintf("column%d", i)
		}
		candidate := name
		for n := 1; used[strings.ToLower(candidate)]; n++ {
			candidate = fmt.Sprintf("%s_%d", name, n)
		}
		used[strings.ToLower(candidate)] = true
		names[i] = candidate
	}
	return names
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func loadCSV(ctx context.Context, db *sql.DB, content []byte) error {
	reader := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(content, []byte("\xef\xbb\xbf"))))
	records, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("read CSV: %w", err)
	}
	if len(records) == 0 {
		return fmt.Errorf("read CSV: no header row")
	}
	names := columnNames(records[0])
	data := records[1:]
	types := inferColumnTypes(data, len(names))

	defs := make([]string, len(names))
	placeholders := make([]string, len(names))
	for i, name := range names {
		defs[i] = quoteIdent(name) + " " + types[i].sqlName()
		placeholders[i] = "?"
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "CREATE TABLE "+tableName+" ("+strings.Join(defs, ", ")+")"); err != nil {
		return err
	}
	insert, err := tx.PrepareContext(ctx, "INSERT INTO "+tableName+" VALUES ("+strings.Join(placeholders, ", ")+")")
	if err != nil {
		return err
	}
	defer insert.Close()
	for _, record := range data {
		values := make([]any, len(record))
		for i, field := range record {
			values[i] = typedField(field, types[i])
		}
		if _, err := insert.ExecContext(ctx, values...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func typedField(field string, t columnType) any {
	if field == "" {
		return nil
	}
	switch t {
	case columnInteger:
		n, _ := strconv.ParseInt(field, 10, 64)
		return n
	case columnReal:
		f, _ := strconv.ParseFloat(field, 64)
		return f
	}
	return field
}

// bindParameters rewrites $name parameters into SQLite placeholders: a list
// becomes a parenthesised placeholder list, any other value a single ?.
func bindParameters(query string, parameters map[string]any) (string, []any, error) {
	var b strings.Builder
	var args []any
	n := len(query)
	for i := 0; i < n; {
		c := query[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			end := i + 1
			for end < n {
				if query[end] == c {
					if end+1 < n && query[end+1] == c {
						end += 2
						continue
					}
					end++
					break
				}
				end++
			}
			b.WriteString(query[i:end])
			i = end
		case c == '-' && i+1 < n && query[i+1] == '-':
			end := strings.IndexByte(query[i:], '\n')
			if end < 0 {
				end = n - i
			}
			b.WriteString(query[i : i+end])
			i += end
		case c == '/' && i+1 < n && query[i+1] == '*':
			end := strings.Index(query[i+2:], "*/")
			if end < 0 {
				b.WriteString(query[i:])
				i = n
			} else {
				b.WriteString(query[i : i+2+end+2])
				i += 2 + end + 2
			}
		case c == '$' && i+1 < n && isIdentStart(query[i+1]):
			end := i + 2
			for end < n && isIdentPart(query[end]) {
				end++
			}
			name := query[i+1 : end]
			value, ok := parameters[name]
			if !ok {
				return "", nil, fmt.Errorf("Invalid Input Error: Values were not provided for the following prepared statement parameters: %s", name)
			}
			placeholder, values, err := placeholderFor(name, value)
			if err != nil {
				return "", nil, err
			}
			b.WriteString(placeholder)
			args = append(args, values...)
			i = end
		case c == '$' && i+1 < n && query[i+1] >= '0' && query[i+1] <= '9':
			return "", nil, fmt.Errorf("positional parameter %q is not supported; use $name with a parameters dict", query[i:min(n, i+3)])
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), args, nil
}

func placeholderFor(name string, value any) (string, []any, error) {
	var items []any
	switch x := value.(type) {
	case []any:
		items = x
	case sandbox.Tuple:
		items = x
	default:
		v, err := scalarParameter(name, value)
		if err != nil {
			return "", nil, err
		}
		return "?", []any{v}, nil
	}
	values := make([]any, len(items))
	marks := make([]string, len(items))
	for i, item := range items {
		v, err := scalarParameter(name, item)
		if err != nil {
			return "", nil, err
		}
		values[i] = v
		marks[i] = "?"
	}
	return "(" + strings.Join(marks, ", ") + ")", values, nil
}

func scalarParameter(name string, v any) (any, error) {
	switch x := v.(type) {
	case nil, bool, int64, float64, string, []byte:
		return x, nil
	case sandbox.Path:
		return string(x), nil
	}
	return nil, monterr.Raise("TypeError", fmt.Sprintf("parameter '%s' has unsupported type %s", name, pyTypeName(v)))
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}
