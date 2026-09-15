package main

import (
	"os"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

func pyDict(kv ...any) *montygo.Dict {
	d := montygo.NewDict()
	for i := 0; i < len(kv); i += 2 {
		d.Set(kv[i], kv[i+1])
	}
	return d
}

func TestRecordModelInfoStubMatchesPydantic(t *testing.T) {
	want, err := os.ReadFile("testdata/record_model_info_stub.txt")
	require.NoError(t, err)
	require.Equal(t, string(want), recordModelInfoStub())
}

func TestModelInfoValidation(t *testing.T) {
	info, err := modelInfoFromDict(pyDict(
		"unique_id", "a", "name", "x", "input_mtok", "3.5", "output_mtok", int64(2),
		"attributes", pyDict("a", int64(1), "b", "x", "c", "2"), "extra", true,
	))
	require.NoError(t, err)
	require.Equal(t, ModelInfo{UniqueID: "a", Name: "x", InputMtok: 3.5, OutputMtok: 2, Attributes: map[string]any{"a": 1.0, "b": "x", "c": "2"}}, info)

	_, err = modelInfoFromDict(pyDict("unique_id", int64(1), "input_mtok", "cheap", "output_mtok", true))
	require.EqualError(t, err, "3 validation errors for ModelInfo\n"+
		"unique_id\n  Input should be a valid string\n"+
		"name\n  Field required\n"+
		"input_mtok\n  Input should be a valid number")
}

func TestFormatAsXML(t *testing.T) {
	got := formatAsXML(pyDict("name", "GPT <X>", "price", pyDict("input", 1.5, "free", false), "tags", []any{"a", nil}))
	require.Equal(t, "<name>GPT &lt;X&gt;</name>\n<price>\n  <input>1.5</input>\n  <free>False</free>\n</price>\n<tags>\n  <item>a</item>\n  <item>null</item>\n</tags>", got)
}

func TestRecordModelInfoValidInputSkipsSubAgent(t *testing.T) {
	llm := &fakeMessages{}
	records := newRecordModels(&subAgent{llm: llm, model: "test-model"})
	got, err := records.recordModelInfo(t.Context(), nil, montygo.Kwargs{"model_information": pyDict("unique_id", "m", "name", "M", "input_mtok", 1.0, "output_mtok", 2.0)})
	require.NoError(t, err)
	require.Equal(t, "Model information recorded successfully for m", got)
	require.Empty(t, llm.requests)
}

func TestRecordModelInfoSubAgentFallback(t *testing.T) {
	invalid := pyDict("name", "GPT X", "price", pyDict("input", "$1.50", "output", "$6"))

	t.Run("coerced into ModelInfo", func(t *testing.T) {
		llm := &fakeMessages{replies: []fakeReply{
			toolUseReply("final_result", map[string]any{"unique_id": "gpt-x", "name": "GPT X", "input_mtok": 1.5, "output_mtok": 6}),
		}}
		records := newRecordModels(&subAgent{llm: llm, model: "test-model"})
		got, err := records.recordModelInfo(t.Context(), []any{invalid}, nil)
		require.NoError(t, err)
		require.Equal(t, "Model information recorded successfully for gpt-x", got)
		require.Equal(t, map[string]ModelInfo{"gpt-x": {UniqueID: "gpt-x", Name: "GPT X", InputMtok: 1.5, OutputMtok: 6}}, records.Models())

		require.Len(t, llm.requests, 1)
		req := llm.requests[0]
		require.Equal(t, "test-model", req.Model)
		require.Equal(t, subAgentInstructions, req.System[0].Text)
		require.Equal(t, "final_result", req.Tools[0].OfTool.Name)
		require.Equal(t, []string{"unique_id", "name", "input_mtok", "output_mtok"}, req.Tools[0].OfTool.InputSchema.Required)
		require.Equal(t, "<name>GPT X</name>\n<price>\n  <input>$1.50</input>\n  <output>$6</output>\n</price>", lastText(t, req.Messages))
	})

	t.Run("text answer is returned", func(t *testing.T) {
		llm := &fakeMessages{replies: []fakeReply{textReply("No unique id could be derived.")}}
		records := newRecordModels(&subAgent{llm: llm, model: "test-model"})
		got, err := records.recordModelInfo(t.Context(), []any{invalid}, nil)
		require.NoError(t, err)
		require.Equal(t, "No unique id could be derived.", got)
		require.Empty(t, records.Models())
	})

	t.Run("invalid output after retry", func(t *testing.T) {
		bad := map[string]any{"name": "GPT X"}
		llm := &fakeMessages{replies: []fakeReply{toolUseReply("final_result", bad), toolUseReply("final_result", bad)}}
		records := newRecordModels(&subAgent{llm: llm, model: "test-model"})
		got, err := records.recordModelInfo(t.Context(), []any{invalid}, nil)
		require.NoError(t, err)
		require.Equal(t, "Error, unable to validate model information", got)
		require.Len(t, llm.requests, 2)
		retry := llm.requests[1].Messages
		require.Len(t, retry, 3)
		result := retry[2].Content[0].OfToolResult
		require.NotNil(t, result)
		require.True(t, result.IsError.Value)
		require.True(t, strings.HasPrefix(result.Content[0].OfText.Text, "3 validation errors for ModelInfo\nunique_id\n  Field required"))
		require.Equal(t, anthropic.MessageParamRoleUser, retry[2].Role)
	})

	t.Run("no API key", func(t *testing.T) {
		_, err := newRecordModels(nil).recordModelInfo(t.Context(), []any{invalid}, nil)
		require.EqualError(t, err, "model information failed validation and the extraction sub-agent needs ANTHROPIC_API_KEY")
	})
}
