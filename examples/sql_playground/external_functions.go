package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	monty "github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/examples/internal/pyargs"
	"github.com/asalimonov/montygo/osaccess"
)

// ExternalFunctions read files through the same OSAccess the sandbox sees.
type ExternalFunctions struct {
	fs *osaccess.OSAccess
}

// queryCSV executes SQL on a CSV file loaded as the table data.
func (e *ExternalFunctions) queryCSV(_ context.Context, args []any, kwargs monty.Kwargs) (any, error) {
	bound, err := pyargs.Bind("query_csv", args, kwargs,
		pyargs.Required("filepath"), pyargs.Required("sql"), pyargs.Optional("parameters", nil))
	if err != nil {
		return nil, err
	}
	path, err := pathArg("filepath", bound[0])
	if err != nil {
		return nil, err
	}
	query, err := pyargs.String("sql", bound[1])
	if err != nil {
		return nil, err
	}
	var parameters map[string]any
	if bound[2] != nil {
		d, ok := bound[2].(*monty.Dict)
		if !ok {
			return nil, monty.Raise("TypeError", "argument 'parameters' must be dict or None, not "+pyTypeName(bound[2]))
		}
		parameters, ok = d.StringMap()
		if !ok {
			return nil, monty.Raise("TypeError", "parameters keys must be str")
		}
	}
	return monty.Async(func() (any, error) {
		content, err := e.fs.PathReadBytes(path)
		if err != nil {
			return nil, err
		}
		return queryCSV(context.Background(), content, query, parameters)
	}), nil
}

// readJSON reads and parses a JSON file.
func (e *ExternalFunctions) readJSON(_ context.Context, args []any, kwargs monty.Kwargs) (any, error) {
	bound, err := pyargs.Bind("read_json", args, kwargs, pyargs.Required("filepath"))
	if err != nil {
		return nil, err
	}
	path, err := pathArg("filepath", bound[0])
	if err != nil {
		return nil, err
	}
	return monty.Async(func() (any, error) {
		content, err := e.fs.PathReadText(path)
		if err != nil {
			return nil, err
		}
		v, err := decodeJSON(content)
		if err != nil {
			return nil, monty.Raise("json.JSONDecodeError", err.Error())
		}
		return v, nil
	}), nil
}

// analyzeSentiment scores text with keyword matching.
func analyzeSentiment(_ context.Context, args []any, kwargs monty.Kwargs) (any, error) {
	bound, err := pyargs.Bind("analyze_sentiment", args, kwargs, pyargs.Required("text"))
	if err != nil {
		return nil, err
	}
	text, err := pyargs.String("text", bound[0])
	if err != nil {
		return nil, err
	}
	return monty.Async(func() (any, error) { return sentimentScore(text), nil }), nil
}

var positiveWords = []string{
	"amazing",
	"great",
	"love",
	"thank",
	"helpful",
	"a+",
	"good",
	"best",
	"excellent",
	"awesome",
	"fantastic",
	"wonderful",
	"glad",
	"enjoy",
	"better",
}

var negativeWords = []string{
	"bad",
	"angry",
	"hate",
	"terrible",
	"worst",
	"fraud",
	"awful",
	"horrible",
	"disappointed",
	"poor",
	"useless",
}

// sentimentScore returns a score from -1.0 (very negative) to +1.0 (very positive).
func sentimentScore(text string) float64 {
	score := 0.0
	lower := strings.ToLower(text)
	for _, word := range positiveWords {
		if strings.Contains(lower, word) {
			score += 0.3
		}
	}
	for _, word := range negativeWords {
		if strings.Contains(lower, word) {
			score -= 0.3
		}
	}
	return max(-1.0, min(1.0, score))
}

func pathArg(name string, v any) (monty.Path, error) {
	switch p := v.(type) {
	case monty.Path:
		return p, nil
	case string:
		return monty.Path(p), nil
	}
	return "", monty.Raise("TypeError", fmt.Sprintf("argument '%s' must be Path or str, not %s", name, pyTypeName(v)))
}

func decodeJSON(text string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	v, err := decodeJSONValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err == nil {
		return nil, fmt.Errorf("extra data after the JSON value")
	}
	return v, nil
}

func decodeJSONValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '[' {
			items := []any{}
			for dec.More() {
				v, err := decodeJSONValue(dec)
				if err != nil {
					return nil, err
				}
				items = append(items, v)
			}
			_, err := dec.Token()
			return items, err
		}
		d := monty.NewDict()
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return nil, err
			}
			v, err := decodeJSONValue(dec)
			if err != nil {
				return nil, err
			}
			d.Set(key, v)
		}
		_, err := dec.Token()
		return d, err
	case json.Number:
		s := t.String()
		if strings.ContainsAny(s, ".eE") {
			return strconv.ParseFloat(s, 64)
		}
		n, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return nil, fmt.Errorf("invalid number %q", s)
		}
		if n.IsInt64() {
			return n.Int64(), nil
		}
		return n, nil
	}
	return tok, nil
}
