package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/examples/internal/pyargs"
)

//go:embed model_info_schema.json
var modelInfoSchema string

// ModelInfo is the structured record of one model and its pricing.
type ModelInfo struct {
	UniqueID    string         `json:"unique_id"`
	Name        string         `json:"name"`
	Description *string        `json:"description"`
	InputMtok   float64        `json:"input_mtok"`
	OutputMtok  float64        `json:"output_mtok"`
	Attributes  map[string]any `json:"attributes"`
}

const subAgentInstructions = "Try to coerce the input into a ModelInfo object, if you're unable to do so, return a string describing the error."

const outputRetries = 1

var errUnableToValidate = errors.New("unable to validate model information")

type modelCoercer interface {
	coerce(ctx context.Context, prompt string) (*ModelInfo, string, error)
}

// RecordModels collects ModelInfo records reported by sandbox code.
type RecordModels struct {
	mu     sync.Mutex
	models map[string]ModelInfo
	agent  modelCoercer
}

func newRecordModels(agent modelCoercer) *RecordModels {
	return &RecordModels{models: map[string]ModelInfo{}, agent: agent}
}

// Models returns a copy of the recorded models keyed by unique id.
func (r *RecordModels) Models() map[string]ModelInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]ModelInfo, len(r.models))
	for k, v := range r.models {
		out[k] = v
	}
	return out
}

func (r *RecordModels) put(info ModelInfo) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.models[info.UniqueID] = info
	return "Model information recorded successfully for " + info.UniqueID
}

// recordModelInfo records information about a model, asking the sub-agent to
// coerce input that fails validation.
func (r *RecordModels) recordModelInfo(ctx context.Context, args []any, kwargs montygo.Kwargs) (any, error) {
	bound, err := pyargs.Bind("record_model_info", args, kwargs, pyargs.Required("model_information"))
	if err != nil {
		return nil, err
	}
	information, ok := bound[0].(*montygo.Dict)
	if !ok {
		return nil, montygo.Raise("TypeError", "record_model_info() argument 'model_information' must be dict, not "+montygo.Repr(bound[0]))
	}
	if info, err := modelInfoFromDict(information); err == nil {
		return r.put(info), nil
	}
	if r.agent == nil {
		return nil, errors.New("model information failed validation and the extraction sub-agent needs ANTHROPIC_API_KEY")
	}
	info, text, err := r.agent.coerce(ctx, formatAsXML(information))
	switch {
	case errors.Is(err, errUnableToValidate):
		return "Error, unable to validate model information", nil
	case err != nil:
		return nil, err
	case info == nil:
		return text, nil
	}
	return r.put(*info), nil
}

func recordModelInfoStub() string {
	return `from typing import Any

def record_model_info(model_information: dict[str, Any]) -> str:
    """Record information about a model.

    NOTE: this method takes a python dict argument, not JSON.

    The dict should have this schema:

    ` + "```json" + `
    ` + indent(modelInfoSchema, "    ") + `
    ` + "```" + `
    """
    ...
`
}

func indent(text, prefix string) string {
	lines := strings.SplitAfter(text, "\n")
	var b strings.Builder
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			b.WriteString(prefix)
		}
		b.WriteString(line)
	}
	return b.String()
}

type validationErrors []string

func (e validationErrors) Error() string {
	plural := "s"
	if len(e) == 1 {
		plural = ""
	}
	return fmt.Sprintf("%d validation error%s for ModelInfo\n%s", len(e), plural, strings.Join(e, "\n"))
}

func (e *validationErrors) add(loc, msg string) {
	*e = append(*e, loc+"\n  "+msg)
}

func modelInfoFromDict(d *montygo.Dict) (ModelInfo, error) {
	var errs validationErrors
	var info ModelInfo
	str := func(key string) string {
		v, ok := d.Get(key)
		if !ok {
			errs.add(key, "Field required")
			return ""
		}
		s, ok := v.(string)
		if !ok {
			errs.add(key, "Input should be a valid string")
		}
		return s
	}
	num := func(key string) float64 {
		v, ok := d.Get(key)
		if !ok {
			errs.add(key, "Field required")
			return 0
		}
		f, ok := laxFloat(v)
		if !ok {
			errs.add(key, "Input should be a valid number")
		}
		return f
	}
	info.UniqueID = str("unique_id")
	info.Name = str("name")
	if v, ok := d.Get("description"); ok && v != nil {
		if s, ok := v.(string); ok {
			info.Description = &s
		} else {
			errs.add("description", "Input should be a valid string")
		}
	}
	info.InputMtok = num("input_mtok")
	info.OutputMtok = num("output_mtok")
	if v, ok := d.Get("attributes"); ok && v != nil {
		attrs, ok := v.(*montygo.Dict)
		if !ok {
			errs.add("attributes", "Input should be a valid dictionary")
		} else {
			info.Attributes = map[string]any{}
			for _, p := range attrs.Pairs() {
				key, ok := p.Key.(string)
				if !ok {
					errs.add("attributes."+montygo.Repr(p.Key)+".[key]", "Input should be a valid string")
					continue
				}
				if s, ok := p.Value.(string); ok {
					info.Attributes[key] = s
				} else if f, ok := laxFloat(p.Value); ok {
					info.Attributes[key] = f
				} else {
					errs.add("attributes."+key, "Input should be a valid number or string")
				}
			}
		}
	}
	if len(errs) > 0 {
		return ModelInfo{}, errs
	}
	return info, nil
}

func laxFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case *big.Int:
		f, _ := new(big.Float).SetInt(x).Float64()
		return f, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

func parseModelInfoJSON(raw string) (ModelInfo, error) {
	v, err := decodeJSON([]byte(raw))
	if err != nil {
		return ModelInfo{}, err
	}
	d, ok := v.(*montygo.Dict)
	if !ok {
		return ModelInfo{}, validationErrors{"\n  Input should be a valid dictionary"}
	}
	return modelInfoFromDict(d)
}

// formatAsXML renders a dict the way pydantic-ai's format_as_xml does.
func formatAsXML(d *montygo.Dict) string {
	var lines []string
	for _, p := range d.Pairs() {
		appendXML(&lines, xmlKey(p.Key), p.Value, "")
	}
	return strings.Join(lines, "\n")
}

func xmlKey(k any) string {
	if s, ok := k.(string); ok {
		return s
	}
	return montygo.Repr(k)
}

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func appendXML(lines *[]string, tag string, v any, prefix string) {
	var items []any
	switch x := v.(type) {
	case *montygo.Dict:
		*lines = append(*lines, prefix+"<"+tag+">")
		for _, p := range x.Pairs() {
			appendXML(lines, xmlKey(p.Key), p.Value, prefix+"  ")
		}
		*lines = append(*lines, prefix+"</"+tag+">")
		return
	case []any:
		items = x
	case montygo.Tuple:
		items = x
	default:
		text := "null"
		switch s := v.(type) {
		case nil:
		case string:
			text = s
		default:
			text = montygo.Repr(v)
		}
		*lines = append(*lines, prefix+"<"+tag+">"+xmlEscaper.Replace(text)+"</"+tag+">")
		return
	}
	*lines = append(*lines, prefix+"<"+tag+">")
	for _, item := range items {
		appendXML(lines, "item", item, prefix+"  ")
	}
	*lines = append(*lines, prefix+"</"+tag+">")
}

type subAgent struct {
	llm   messagesAPI
	model string
}

func (a *subAgent) finalResultTool() (anthropic.ToolParam, error) {
	var schema struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if err := json.Unmarshal([]byte(modelInfoSchema), &schema); err != nil {
		return anthropic.ToolParam{}, err
	}
	return anthropic.ToolParam{
		Name:        "final_result",
		Description: anthropic.String("The final response which ends this conversation"),
		InputSchema: anthropic.ToolInputSchemaParam{Properties: schema.Properties, Required: schema.Required},
	}, nil
}

func (a *subAgent) coerce(ctx context.Context, prompt string) (*ModelInfo, string, error) {
	tool, err := a.finalResultTool()
	if err != nil {
		return nil, "", err
	}
	messages := []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(prompt))}
	for attempt := 0; attempt <= outputRetries; attempt++ {
		resp, err := a.llm.New(ctx, anthropic.MessageNewParams{
			Model:     a.model,
			MaxTokens: 16000,
			System:    []anthropic.TextBlockParam{{Text: subAgentInstructions}},
			Tools:     []anthropic.ToolUnionParam{{OfTool: &tool}},
			Messages:  messages,
		})
		if err != nil {
			return nil, "", fmt.Errorf("extraction sub-agent: %w", err)
		}
		if resp.StopReason == anthropic.StopReasonRefusal {
			return nil, "", fmt.Errorf("extraction sub-agent refused: %s", resp.StopDetails.Explanation)
		}
		messages = append(messages, resp.ToParam())
		var texts []string
		var results []anthropic.ContentBlockParamUnion
		var model *ModelInfo
		for _, block := range resp.Content {
			switch b := block.AsAny().(type) {
			case anthropic.TextBlock:
				texts = append(texts, b.Text)
			case anthropic.ToolUseBlock:
				if b.Name != tool.Name {
					results = append(results, anthropic.NewToolResultBlock(b.ID, fmt.Sprintf("Unknown tool name: %q", b.Name), true))
					continue
				}
				info, err := parseModelInfoJSON(b.JSON.Input.Raw())
				if err != nil {
					results = append(results, anthropic.NewToolResultBlock(b.ID, err.Error()+"\n\nFix the errors and try again.", true))
					continue
				}
				if model == nil {
					model = &info
				}
				results = append(results, anthropic.NewToolResultBlock(b.ID, "Final result processed.", false))
			}
		}
		if model != nil {
			return model, "", nil
		}
		if len(results) == 0 {
			return nil, strings.Join(texts, "\n\n"), nil
		}
		messages = append(messages, anthropic.NewUserMessage(results...))
	}
	return nil, "", errUnableToValidate
}

func sortedModelIDs(models map[string]ModelInfo) []string {
	ids := make([]string, 0, len(models))
	for id := range models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
