package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/examples/internal/montyenv"
)

const pricingPage = `<!DOCTYPE html>
<html><head><title>Pricing</title></head>
<body>
<h1>Model pricing</h1>
<table id="pricing">
  <tr><th>Model</th><th>Input</th><th>Output</th></tr>
  <tr><td>Alpha</td><td>$1 / MTok</td><td>$5 / MTok</td></tr>
  <tr><td>Beta (deprecated)</td><td>$2 / MTok</td><td>$6 / MTok</td></tr>
  <tr><td>Gamma</td><td>$3 / MTok</td><td>$15 / MTok</td><td>batch only</td></tr>
  <tr><td colspan="3">Prices in USD</td></tr>
</table>
<table><tr><td>lonely</td></tr></table>
<script>
  const row = document.createElement('tr');
  row.innerHTML = '<td>Delta</td><td>$0.25 / MTok</td><td>$1.25 / MTok</td>';
  document.querySelector('#pricing tbody').appendChild(row);
</script>
</body></html>`

const (
	alphaRepr = `{'Model': 'Alpha', 'Input': '$1 / MTok', 'Output': '$5 / MTok'}`
	gammaRepr = `{'Model': 'Gamma', 'Input': '$3 / MTok', 'Output': '$15 / MTok', 'column_3': 'batch only'}`
	deltaRepr = `{'Model': 'Delta', 'Input': '$0.25 / MTok', 'Output': '$1.25 / MTok'}`
	alphaJSON = `{"Model":"Alpha","Input":"$1 / MTok","Output":"$5 / MTok"}`
	gammaJSON = `{"Model":"Gamma","Input":"$3 / MTok","Output":"$15 / MTok","column_3":"batch only"}`
	deltaJSON = `{"Model":"Delta","Input":"$0.25 / MTok","Output":"$1.25 / MTok"}`
)

func examplePrintOutput(models ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Found %d models with pricing data\n\nModel pricing information:\n", len(models))
	for i, m := range models {
		fmt.Fprintf(&b, "\n%d. %s\n", i+1, m)
	}
	return b.String()
}

func newTestPool(t *testing.T) *montygo.Pool {
	t.Helper()
	pool, err := montygo.New(t.Context(), montyenv.PoolOptions())
	require.NoError(t, err)
	t.Logf("monty backend: %s", pool.Backend())
	t.Cleanup(func() { _ = pool.Close(context.Background()) })
	return pool
}

func requireBrowser(t *testing.T) *Browser {
	t.Helper()
	browser, err := startBrowser(t.Context())
	if err != nil {
		t.Skipf("headless Chrome unavailable: %v", err)
	}
	t.Cleanup(browser.Close)
	return browser
}

type fakeReply struct {
	content string
	stop    string
}

func textReply(text string) fakeReply {
	b, _ := json.Marshal([]map[string]any{{"type": "text", "text": text}})
	return fakeReply{content: string(b), stop: "end_turn"}
}

func toolUseReply(name string, input any) fakeReply {
	b, _ := json.Marshal([]map[string]any{{"type": "tool_use", "id": "toolu_01", "name": name, "input": input}})
	return fakeReply{content: string(b), stop: "tool_use"}
}

type fakeMessages struct {
	replies  []fakeReply
	requests []anthropic.MessageNewParams
}

func (f *fakeMessages) New(_ context.Context, params anthropic.MessageNewParams, _ ...option.RequestOption) (*anthropic.Message, error) {
	f.requests = append(f.requests, params)
	if len(f.replies) == 0 {
		return nil, errors.New("fake LLM: no scripted reply left")
	}
	reply := f.replies[0]
	f.replies = f.replies[1:]
	raw := fmt.Sprintf(`{"id":"msg_%02d","type":"message","role":"assistant","model":%q,"content":%s,"stop_reason":%q,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`,
		len(f.requests), params.Model, reply.content, reply.stop)
	var msg anthropic.Message
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func lastText(t *testing.T, messages []anthropic.MessageParam) string {
	t.Helper()
	require.NotEmpty(t, messages)
	content := messages[len(messages)-1].Content
	require.NotEmpty(t, content)
	require.NotNil(t, content[0].OfText)
	return content[0].OfText.Text
}

func TestExtractCode(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := []struct {
		name     string
		response string
		want     ExtractCode
	}{
		{"python fence with comment", "Let me look.\n```python\nx = 1\n```\ntrailing", ExtractCode{Code: str("x = 1"), Comment: str("Let me look.")}},
		{"py fence", "```py\n  y = 2  \n```", ExtractCode{Code: str("y = 2")}},
		{"python fence wins over earlier generic fence", "```text\nnotes\n```\n```python\nz = 3\n```", ExtractCode{Code: str("z = 3"), Comment: str("```text\nnotes\n```")}},
		{"any fence", "Run this:\n```\nprint(1)\n```", ExtractCode{Code: str("print(1)"), Comment: str("Run this:")}},
		{"language tag with trailing spaces", "```sh  \nls\n```", ExtractCode{Code: str("ls")}},
		{"no fence", "  All done.\n", ExtractCode{Comment: str("All done.")}},
		{"unterminated fence", "```python\nx = 1", ExtractCode{Comment: str("```python\nx = 1")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, extractCode(tc.response))
		})
	}
}

func TestStubsAndInstructions(t *testing.T) {
	require.True(t, strings.HasPrefix(stubs, "\nfrom typing import Any\n\ndef record_model_info(model_information: dict[str, Any]) -> str:\n"))
	require.Contains(t, stubs, "\n\nimport re\nfrom .browser import PwPage as PwPage\n")
	require.True(t, strings.HasSuffix(instructions, "```python\n"+stubs+"\n```\n"))
	require.NotContains(t, instructions, "{stubs}")
	require.Contains(t, strings.ReplaceAll(promptTemplate, "{url}", urls["anthropic"]), "\n\nhttps://platform.claude.com/docs/en/about-claude/pricing\n\n")
}

func TestRunAgentModeRequiresAPIKey(t *testing.T) {
	t.Setenv(apiKeyEnv, "")
	err := run(t.Context(), io.Discard, nil)
	require.EqualError(t, err, "ANTHROPIC_API_KEY is not set: agent mode calls the Anthropic Messages API; set the key, or pass -code to run code without the LLM")
}

func TestAgentLoopWithFakeLLM(t *testing.T) {
	llm := &fakeMessages{replies: []fakeReply{
		textReply("I'll record the model.\n\n```python\nresult = record_model_info({'unique_id': 'm1', 'name': 'Model One', 'input_mtok': 1, 'output_mtok': 2.5})\nprint(result)\nresult\n```"),
		textReply("```python\nrecord_model_info('not a dict')\n```"),
		textReply("```py\nraise ValueError('boom')\n```"),
		textReply("All models recorded."),
	}}
	records := newRecordModels(nil)
	var out bytes.Buffer
	s := &scraper{
		llm:   llm,
		model: "test-model",
		pool:  newTestPool(t),
		out:   &out,
		externals: map[string]any{
			"beautiful_soup":    montygo.FunctionFunc(beautifulSoup),
			"record_model_info": montygo.FunctionFunc(records.recordModelInfo),
		},
	}

	require.NoError(t, s.runAgent(t.Context(), "scrape it"))

	require.Equal(t, "LLM: I'll record the model.\nLLM: All models recorded.\ndone\n", out.String())
	require.Len(t, llm.requests, 4)
	for i, req := range llm.requests {
		require.Equal(t, "test-model", req.Model)
		require.Len(t, req.System, 1)
		require.Equal(t, instructions, req.System[0].Text)
		require.Len(t, req.Messages, 2*i+1)
	}
	require.Equal(t, "scrape it", lastText(t, llm.requests[0].Messages))
	require.Equal(t, anthropic.MessageParamRoleAssistant, llm.requests[1].Messages[1].Role)
	require.Contains(t, llm.requests[1].Messages[1].Content[0].OfText.Text, "record_model_info({'unique_id': 'm1'")
	require.Equal(t,
		`"Model information recorded successfully for m1"`+"\n\nPrint Output:\n---\nModel information recorded successfully for m1\n\n---",
		lastText(t, llm.requests[1].Messages))

	typing := lastText(t, llm.requests[2].Messages)
	require.True(t, strings.HasPrefix(typing, "Error Preparing Code: "), typing)
	require.Contains(t, typing, "record_model_info")

	runtime := lastText(t, llm.requests[3].Messages)
	require.True(t, strings.HasPrefix(runtime, "Error running code: Traceback (most recent call last):"), runtime)
	require.True(t, strings.HasSuffix(runtime, "ValueError: boom"), runtime)

	models := records.Models()
	require.Equal(t, map[string]ModelInfo{"m1": {UniqueID: "m1", Name: "Model One", InputMtok: 1, OutputMtok: 2.5}}, models)
}

func TestAgentLoopReportsAPIErrors(t *testing.T) {
	s := &scraper{llm: &fakeMessages{}, model: "test-model", out: io.Discard}
	require.EqualError(t, s.runAgent(t.Context(), "prompt"), "scrape agent: fake LLM: no scripted reply left")
}

func TestExampleCodeOnStaticPage(t *testing.T) {
	page := &Page{URL: "http://fixture.test/", Title: "Pricing", HTML: pricingPage, ID: 1}
	openPage := func(_ context.Context, args []any, kwargs montygo.Kwargs) (any, error) {
		return montygo.Async(func() (any, error) { return page.instance() }), nil
	}
	s := &scraper{
		pool: newTestPool(t),
		out:  io.Discard,
		externals: map[string]any{
			"open_page":         montygo.FunctionFunc(openPage),
			"beautiful_soup":    montygo.FunctionFunc(beautifulSoup),
			"record_model_info": montygo.FunctionFunc(newRecordModels(nil).recordModelInfo),
		},
	}
	msg, err := s.runCode(t.Context(), exampleCode, map[string]any{"url": page.URL}, false)
	require.NoError(t, err)
	require.Equal(t, "["+alphaJSON+","+gammaJSON+"]\n\nPrint Output:\n---\n"+examplePrintOutput(alphaRepr, gammaRepr)+"\n---", msg)

	msg, err = s.runCode(t.Context(), exampleCode, map[string]any{"url": page.URL}, true)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(msg, "Error Preparing Code: error[unresolved-reference]: Name `url` used when not defined"), msg)
	require.Contains(t, msg, "error[invalid-argument-type]: Argument to bound method `Tag.find_all` is incorrect")
	require.Contains(t, msg, "Expected `str | Pattern[str] | None`, found `list[str]`")
}

func TestExampleCodeInHeadlessChrome(t *testing.T) {
	requireBrowser(t)
	t.Setenv(apiKeyEnv, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, pricingPage)
	}))
	defer srv.Close()

	var out bytes.Buffer
	require.NoError(t, run(t.Context(), &out, []string{"-code", "-url", srv.URL}))

	want := "[" + alphaJSON + "," + gammaJSON + "," + deltaJSON + "]" +
		"\n\nPrint Output:\n---\n" + examplePrintOutput(alphaRepr, gammaRepr, deltaRepr) + "\n---\n" +
		"models=0\n"
	require.Equal(t, want, out.String())
}

func TestRunCodeFile(t *testing.T) {
	requireBrowser(t)
	t.Setenv(apiKeyEnv, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<html><head><title>Tiny</title></head><body><p class='x'>hello</p></body></html>")
	}))
	defer srv.Close()
	file := t.TempDir() + "/scrape.py"
	code := "page = await open_page(url)\nsoup = beautiful_soup(page.html)\nrecord_model_info({'unique_id': page.title, 'name': soup.select_one('p.x').get_text(), 'input_mtok': 1, 'output_mtok': 2})\n"
	require.NoError(t, writeFile(file, code))

	var out bytes.Buffer
	require.NoError(t, run(t.Context(), &out, []string{"-code", file, "-url", srv.URL}))
	require.Equal(t, `"Model information recorded successfully for Tiny"`+"\nmodels=1\n"+`{"unique_id":"Tiny","name":"hello","description":null,"input_mtok":1,"output_mtok":2,"attributes":null}`+"\n", out.String())
}
