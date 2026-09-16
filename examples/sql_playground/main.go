// Command sql_playground joins CSV purchase data with JSON tweets inside the
// sandbox through SQL, JSON and sentiment host functions over a virtual filesystem.
package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/examples/internal/montyenv"
	"github.com/asalimonov/montygo/runtime/osaccess"
)

//go:embed sandbox_code.py
var sandboxCode string

//go:embed type_stubs.pyi
var typeStubs string

const datasetsEnv = "MAFUDGE_DATASETS"

var mountedFiles = []struct {
	virtual string
	host    string
}{
	{"/data/customers/customers.csv", "customers/customers.csv"},
	{"/data/customers/surveys.csv", "customers/surveys.csv"},
	{"/data/tweets/tweets.json", "tweets/tweets.json"},
}

func main() {
	err := run(context.Background(), os.Stdout, os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		var montyErr montygo.Error
		if errors.As(err, &montyErr) {
			fmt.Fprintln(os.Stderr, montyErr.Display(montygo.DisplayTraceback))
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}

func defaultDatasetsDir() string {
	if dir := os.Getenv(datasetsEnv); dir != "" {
		return dir
	}
	_, file, _, _ := runtime.Caller(0)
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	return filepath.Join(repoRoot, "..", "mafudge_datasets")
}

func run(ctx context.Context, out io.Writer, args []string) error {
	flags := flag.NewFlagSet("sql_playground", flag.ContinueOnError)
	datasets := flags.String("datasets", defaultDatasetsDir(), "mafudge datasets checkout (env "+datasetsEnv+")")
	typeCheck := flags.Bool("type-check", false, "type-check the sandbox code against the stubs before running it; upstream sandbox_code.py fails because session stubs are not importable as type_stubs")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	dir, err := filepath.Abs(*datasets)
	if err != nil {
		return err
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("mafudge_datasets directory not found at %s. ", dir)
	}

	files := make([]osaccess.File, 0, len(mountedFiles))
	for _, f := range mountedFiles {
		content, err := os.ReadFile(filepath.Join(dir, f.host))
		if err != nil {
			return err
		}
		files = append(files, osaccess.NewMemoryFile(f.virtual, string(content)))
	}
	fs, err := osaccess.New(files, nil)
	if err != nil {
		return err
	}
	external := &ExternalFunctions{fs: fs}

	pool, err := montygo.New(ctx, montyenv.PoolOptions())
	if err != nil {
		return err
	}
	defer pool.Close(ctx)

	results, err := func() (any, error) {
		session, err := pool.Checkout(ctx, montygo.CheckoutOptions{
			ScriptName:     "sql_playground.py",
			TypeCheck:      true,
			TypeCheckStubs: typeStubs,
		})
		if err != nil {
			return nil, err
		}
		defer session.Close(ctx)
		return session.FeedRun(ctx, sandboxCode, &montygo.FeedOptions{
			ExternalLookup: map[string]any{
				"query_csv":         montygo.FunctionFunc(external.queryCSV),
				"read_json":         montygo.FunctionFunc(external.readJSON),
				"analyze_sentiment": montygo.FunctionFunc(analyzeSentiment),
			},
			OS:            fs.Handler(),
			SkipTypeCheck: !*typeCheck,
			Print: montygo.PrintFunc(func(stream montygo.Stream, text string) error {
				w := out
				if stream == montygo.Stderr {
					w = os.Stderr
				}
				_, err := io.WriteString(w, text)
				return err
			}),
		})
	}()
	if err != nil {
		return err
	}
	return printReport(out, results)
}

func printReport(out io.Writer, results any) error {
	items, isList := results.([]any)
	if results == nil || (isList && len(items) == 0) {
		fmt.Fprintln(out, "No results found. Check if customers have matching Twitter handles and tweets.")
	}
	if !isList {
		return fmt.Errorf("TypeError: '%s' object is not iterable", pyTypeName(results))
	}
	for _, item := range items {
		r, ok := item.(*montygo.Dict)
		if !ok {
			return fmt.Errorf("TypeError: result rows must be dicts, got %s", pyTypeName(item))
		}
		fields := map[string]any{}
		for _, key := range []string{"name", "total_purchases", "twitter", "tweet_count", "avg_sentiment"} {
			v, ok := r.Get(key)
			if !ok {
				return fmt.Errorf("KeyError: '%s'", key)
			}
			fields[key] = v
		}
		avg, ok := asFloat(fields["avg_sentiment"])
		if !ok {
			return fmt.Errorf("TypeError: avg_sentiment must be a number, got %s", pyTypeName(fields["avg_sentiment"]))
		}
		purchases, err := thousands(fields["total_purchases"])
		if err != nil {
			return err
		}
		emoji := "😞"
		switch {
		case avg > 0:
			emoji = "😊"
		case avg == 0:
			emoji = "😐"
		}
		fmt.Fprintf(out, "  %s\n", pyStr(fields["name"]))
		fmt.Fprintf(out, "    Purchases: $%s\n", purchases)
		fmt.Fprintf(out, "    Twitter: @%s\n", pyStr(fields["twitter"]))
		fmt.Fprintf(out, "    Tweets: %s\n", pyStr(fields["tweet_count"]))
		fmt.Fprintf(out, "    Sentiment: %+.2f %s\n", avg, emoji)
		fmt.Fprintln(out)
	}
	return nil
}

func pyStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return montygo.Repr(v)
}

func pyTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "NoneType"
	case string:
		return "str"
	case int64, *big.Int:
		return "int"
	case float64:
		return "float"
	case bool:
		return "bool"
	case []any:
		return "list"
	case *montygo.Dict:
		return "dict"
	}
	return fmt.Sprintf("%T", v)
}

func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// thousands renders a number the way Python's format(value, ',') does.
func thousands(v any) (string, error) {
	switch x := v.(type) {
	case int64:
		return groupDigits(strconv.FormatInt(x, 10)), nil
	case *big.Int:
		return groupDigits(x.String()), nil
	case bool:
		if x {
			return "1", nil
		}
		return "0", nil
	case float64:
		s := montygo.Repr(x)
		if strings.ContainsAny(s, "eni") {
			return s, nil
		}
		whole, frac, _ := strings.Cut(s, ".")
		return groupDigits(whole) + "." + frac, nil
	}
	return "", fmt.Errorf("ValueError: Cannot specify ',' with '%s'", pyTypeName(v))
}

func groupDigits(s string) string {
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return sign + b.String()
}
