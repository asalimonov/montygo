// Command web_scraper lets an LLM write Monty code that extracts model pricing
// from a web page through a headless browser and BeautifulSoup-like helpers.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/examples/internal/montyenv"
)

//go:embed example_code.py
var exampleCode string

//go:embed external_functions.pyi
var externalFunctionsStub string

//go:embed instructions.md
var instructionsTemplate string

//go:embed prompt.md
var promptTemplate string

const (
	defaultModel = "claude-sonnet-4-5"
	modelEnv     = "WEB_SCRAPER_MODEL"
	apiKeyEnv    = "ANTHROPIC_API_KEY"
)

var urls = map[string]string{
	"openai":    "https://developers.openai.com/api/docs/pricing",
	"anthropic": "https://platform.claude.com/docs/en/about-claude/pricing",
	"groq":      "https://groq.com/pricing",
}

var (
	stubs        = "\n" + recordModelInfoStub() + "\n\n" + externalFunctionsStub + "\n"
	instructions = strings.ReplaceAll(instructionsTemplate, "{stubs}", stubs)
)

type messagesAPI interface {
	New(ctx context.Context, params anthropic.MessageNewParams, opts ...option.RequestOption) (*anthropic.Message, error)
}

func main() {
	err := run(context.Background(), os.Stdout, os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type codeFlag struct {
	set  bool
	path string
}

func (c *codeFlag) String() string { return c.path }

func (c *codeFlag) Set(v string) error {
	c.set = true
	if v != "true" {
		c.path = v
	}
	return nil
}

func (c *codeFlag) IsBoolFlag() bool { return true }

func run(ctx context.Context, out io.Writer, args []string) error {
	fs := flag.NewFlagSet("web_scraper", flag.ContinueOnError)
	provider := fs.String("provider", "anthropic", "pricing page to scrape: openai, anthropic or groq")
	pageURL := fs.String("url", "", "page to scrape instead of the provider's pricing page")
	model := fs.String("model", envOr(modelEnv, defaultModel), "Claude model for the scrape agent and the extraction sub-agent (env "+modelEnv+")")
	typeCheck := fs.Bool("type-check", false, "type-check the -code source against the stubs, as agent mode always does")
	var code codeFlag
	fs.Var(&code, "code", "run `file` directly instead of asking the LLM; without a file runs the embedded example_code.py")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if code.set && code.path == "" && fs.NArg() > 0 {
		code.path = fs.Arg(0)
		if err := fs.Parse(fs.Args()[1:]); err != nil {
			return err
		}
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	target := *pageURL
	if target == "" {
		var ok bool
		if target, ok = urls[*provider]; !ok {
			return fmt.Errorf("unknown provider %q", *provider)
		}
	}

	var llm messagesAPI
	if key := os.Getenv(apiKeyEnv); key != "" {
		client := anthropic.NewClient(option.WithAPIKey(key))
		llm = &client.Messages
	}
	if llm == nil && !code.set {
		return errors.New(apiKeyEnv + " is not set: agent mode calls the Anthropic Messages API; set the key, or pass -code to run code without the LLM")
	}
	source := exampleCode
	if code.path != "" {
		b, err := os.ReadFile(code.path)
		if err != nil {
			return err
		}
		source = string(b)
	}

	pool, err := montygo.New(ctx, montyenv.PoolOptions())
	if err != nil {
		return err
	}
	defer pool.Close(ctx)
	browser, err := startBrowser(ctx)
	if err != nil {
		return err
	}
	defer browser.Close()

	var coercer modelCoercer
	if llm != nil {
		coercer = &subAgent{llm: llm, model: *model}
	}
	records := newRecordModels(coercer)
	s := &scraper{
		llm:   llm,
		model: *model,
		pool:  pool,
		out:   out,
		externals: map[string]any{
			"open_page":         montygo.FunctionFunc(browser.openPage),
			"beautiful_soup":    montygo.FunctionFunc(beautifulSoup),
			"record_model_info": montygo.FunctionFunc(records.recordModelInfo),
		},
	}

	if code.set {
		msg, err := s.runCode(ctx, source, map[string]any{"url": target}, *typeCheck)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, msg)
	} else if err := s.runAgent(ctx, strings.ReplaceAll(promptTemplate, "{url}", target)); err != nil {
		return err
	}

	models := records.Models()
	fmt.Fprintf(out, "models=%d\n", len(models))
	for _, id := range sortedModelIDs(models) {
		b, err := json.Marshal(models[id])
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "%s\n", b)
	}
	return nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

type scraper struct {
	llm       messagesAPI
	model     string
	pool      *montygo.Pool
	externals map[string]any
	out       io.Writer
}

func (s *scraper) runAgent(ctx context.Context, prompt string) error {
	messages := []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(prompt))}
	for {
		resp, err := s.llm.New(ctx, anthropic.MessageNewParams{
			Model:     s.model,
			MaxTokens: 16000,
			System:    []anthropic.TextBlockParam{{Text: instructions}},
			Messages:  messages,
		})
		if err != nil {
			return fmt.Errorf("scrape agent: %w", err)
		}
		if resp.StopReason == anthropic.StopReasonRefusal {
			return fmt.Errorf("scrape agent refused: %s", resp.StopDetails.Explanation)
		}
		messages = append(messages, resp.ToParam())

		extracted := extractCode(responseText(resp))
		if extracted.Comment != nil {
			fmt.Fprintf(s.out, "LLM: %s\n", *extracted.Comment)
		}
		if extracted.Code == nil {
			fmt.Fprintln(s.out, "done")
			return nil
		}

		msg, err := s.runCode(ctx, *extracted.Code, nil, true)
		if err != nil {
			return err
		}
		messages = append(messages, anthropic.NewUserMessage(anthropic.NewTextBlock(msg)))
	}
}

func responseText(resp *anthropic.Message) string {
	var parts []string
	for _, block := range resp.Content {
		if text, ok := block.AsAny().(anthropic.TextBlock); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// runCode runs code in a fresh session, type-checked against the stubs when
// typeCheck is set, and renders the outcome as the message the LLM receives next.
func (s *scraper) runCode(ctx context.Context, code string, inputs map[string]any, typeCheck bool) (string, error) {
	var printOutput strings.Builder
	output, err := func() (any, error) {
		session, err := s.pool.Checkout(ctx, montygo.CheckoutOptions{TypeCheck: true, TypeCheckStubs: stubs})
		if err != nil {
			return nil, err
		}
		defer session.Close(ctx)
		return session.FeedRun(ctx, code, &montygo.FeedOptions{
			Inputs:         inputs,
			ExternalLookup: s.externals,
			SkipTypeCheck:  !typeCheck,
			Print: montygo.PrintFunc(func(_ montygo.Stream, text string) error {
				printOutput.WriteString(text)
				return nil
			}),
		})
	}()

	var msg string
	var runtimeErr *montygo.RuntimeError
	var typingErr *montygo.TypingError
	var montyErr montygo.Error
	switch {
	case errors.As(err, &runtimeErr):
		msg = "Error running code: " + runtimeErr.Display(montygo.DisplayTraceback)
	case errors.As(err, &typingErr):
		msg = "Error Preparing Code: " + typingErr.Display(montygo.DisplayTraceback)
	case errors.As(err, &montyErr):
		msg = "Error Preparing Code: " + montyErr.Error()
	case err != nil:
		return "", err
	default:
		msg = toJSON(output)
	}
	if printOutput.Len() > 0 {
		msg += "\n\nPrint Output:\n---\n" + printOutput.String() + "\n---"
	}
	return msg, nil
}

// ExtractCode is Python code extracted from an LLM response.
//
// Priority: the first python or py code fence, then the first code fence of any
// language; without a fence the whole response is the comment.
type ExtractCode struct {
	Code    *string
	Comment *string
}

var (
	pythonFence = regexp.MustCompile("(?s)```(?:python|py)\\s*\\n(.*?)```")
	anyFence    = regexp.MustCompile("(?s)```\\w*\\s*\\n(.*?)```")
)

func extractCode(response string) ExtractCode {
	m := pythonFence.FindStringSubmatchIndex(response)
	if m == nil {
		m = anyFence.FindStringSubmatchIndex(response)
	}
	if m != nil {
		code := strings.TrimSpace(response[m[2]:m[3]])
		var comment *string
		if c := strings.TrimSpace(response[:m[0]]); c != "" {
			comment = &c
		}
		return ExtractCode{Code: &code, Comment: comment}
	}
	comment := strings.TrimSpace(response)
	return ExtractCode{Comment: &comment}
}

type dictable interface {
	asDict() *montygo.Dict
}

func toJSON(v any) string {
	var b strings.Builder
	writeJSON(&b, v)
	return b.String()
}

func writeJSON(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case *big.Int:
		b.WriteString(x.String())
	case float64:
		switch {
		case math.IsNaN(x):
			b.WriteString("NaN")
		case math.IsInf(x, 1):
			b.WriteString("Infinity")
		case math.IsInf(x, -1):
			b.WriteString("-Infinity")
		default:
			b.WriteString(strings.Replace(montygo.Repr(x), "e+", "e", 1))
		}
	case string:
		writeJSONString(b, x)
	case []byte:
		writeJSONString(b, string(x))
	case []any:
		writeJSONArray(b, x)
	case montygo.Tuple:
		writeJSONArray(b, x)
	case *montygo.Set:
		writeJSONArray(b, x.Items())
	case *montygo.FrozenSet:
		writeJSONArray(b, x.Items())
	case *montygo.Dict:
		b.WriteByte('{')
		for i, p := range x.Pairs() {
			if i > 0 {
				b.WriteByte(',')
			}
			key, ok := p.Key.(string)
			if !ok {
				key = montygo.Repr(p.Key)
			}
			writeJSONString(b, key)
			b.WriteByte(':')
			writeJSON(b, p.Value)
		}
		b.WriteByte('}')
	case *montygo.ClassProxy:
		writeJSON(b, x.Attributes)
	case dictable:
		writeJSON(b, x.asDict())
	default:
		writeJSONString(b, montygo.Repr(x))
	}
}

func writeJSONArray(b *strings.Builder, items []any) {
	b.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSON(b, item)
	}
	b.WriteByte(']')
}

func writeJSONString(b *strings.Builder, s string) {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	b.WriteString(strings.TrimSuffix(buf.String(), "\n"))
}
