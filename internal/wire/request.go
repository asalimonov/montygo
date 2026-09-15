package wire

import (
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/asalimonov/montygo/internal/value"
)

// Request is a parent-to-child protocol request.
type Request interface {
	requestField() protowire.Number
	appendBody(b []byte) ([]byte, error)
	// Name identifies the request kind in diagnostics.
	Name() string
}

// Limits mirrors monty.v1.ResourceLimits; nil fields are absent.
type Limits struct {
	MaxDurationMicros *uint64
	MaxMemoryBytes    *uint64
	GCInterval        *uint64
	MaxRecursionDepth *uint64
	MaxSuspensions    *uint64
}

// Configure opens a session on a worker.
type Configure struct {
	ScriptName               string
	Limits                   *Limits
	TypeCheck                bool
	TypeCheckStubs           *string
	MontyVersion             string
	AssertMessageAnnotations *uint32
	TypeCheckFormat          int32
	TypeCheckColor           bool
	ProtocolVersion          uint32
	PrintFlushIntervalMs     *uint32
}

// InstallDependencies asks the worker to install packages.
type InstallDependencies struct{ Requirements []string }

// NamedValue is one feed input.
type NamedValue struct {
	Name  string
	Value any
}

// Feed runs one snippet.
type Feed struct {
	Code          string
	Inputs        []NamedValue
	SkipTypeCheck bool
	Cwd           string
}

// ExtKind selects the ExtFunctionResult arm.
type ExtKind uint8

const (
	ExtReturn ExtKind = iota + 1
	ExtError
	ExtFuture
	ExtNotFound
	ExtNotHandled
)

// ExtResult is the outcome of an external call.
type ExtResult struct {
	Kind         ExtKind
	Value        any
	Error        *Exception
	FutureCallID uint32
	NotFoundName string
}

// ResumeCall answers a FunctionCall or OsCall.
type ResumeCall struct {
	CallID uint32
	Result ExtResult
}

// LookupKind selects the ResumeNameLookup arm.
type LookupKind uint8

const (
	LookupValue LookupKind = iota + 1
	LookupUndefined
	LookupError
)

// ResumeNameLookup answers a NameLookup.
type ResumeNameLookup struct {
	Kind  LookupKind
	Value any
	Error *Exception
}

// FutureResult is one resolved future.
type FutureResult struct {
	CallID uint32
	Result ExtResult
}

// ResumeFutures answers ResolveFutures (or an eager call).
type ResumeFutures struct{ Results []FutureResult }

// Dump requests a session snapshot.
type Dump struct{}

// Load restores a snapshot.
type Load struct{ State []byte }

// Reset ends the checkout.
type Reset struct{}

// Shutdown asks the worker to exit.
type Shutdown struct{}

// AbortFeed raises an uncatchable exception at the pending suspension.
type AbortFeed struct{ Exception *Exception }

func (Configure) requestField() protowire.Number           { return 1 }
func (InstallDependencies) requestField() protowire.Number { return 2 }
func (Feed) requestField() protowire.Number                { return 3 }
func (ResumeCall) requestField() protowire.Number          { return 4 }
func (ResumeNameLookup) requestField() protowire.Number    { return 5 }
func (ResumeFutures) requestField() protowire.Number       { return 6 }
func (Dump) requestField() protowire.Number                { return 7 }
func (Load) requestField() protowire.Number                { return 8 }
func (Reset) requestField() protowire.Number               { return 9 }
func (Shutdown) requestField() protowire.Number            { return 10 }
func (AbortFeed) requestField() protowire.Number           { return 11 }

func (Configure) Name() string           { return "Configure" }
func (InstallDependencies) Name() string { return "InstallDependencies" }
func (Feed) Name() string                { return "Feed" }
func (ResumeCall) Name() string          { return "ResumeCall" }
func (ResumeNameLookup) Name() string    { return "ResumeNameLookup" }
func (ResumeFutures) Name() string       { return "ResumeFutures" }
func (Dump) Name() string                { return "Dump" }
func (Load) Name() string                { return "Load" }
func (Reset) Name() string               { return "Reset" }
func (Shutdown) Name() string            { return "Shutdown" }
func (AbortFeed) Name() string           { return "AbortFeed" }

// EncodeRequest encodes a ParentRequest message.
func EncodeRequest(req Request, traceParent string) ([]byte, error) {
	body, err := req.appendBody(nil)
	if err != nil {
		return nil, err
	}
	out := appendMessage(nil, req.requestField(), body)
	if traceParent != "" {
		out = appendStringAlways(out, 20, traceParent)
	}
	return out, nil
}

func appendOptionalU64(b []byte, num protowire.Number, v *uint64) []byte {
	if v == nil {
		return b
	}
	return appendVarintField(b, num, *v)
}

func (c Configure) appendBody(b []byte) ([]byte, error) {
	b = appendStringField(b, 1, c.ScriptName)
	if c.Limits != nil {
		var l []byte
		l = appendOptionalU64(l, 1, c.Limits.MaxDurationMicros)
		l = appendOptionalU64(l, 2, c.Limits.MaxMemoryBytes)
		l = appendOptionalU64(l, 3, c.Limits.GCInterval)
		l = appendOptionalU64(l, 4, c.Limits.MaxRecursionDepth)
		l = appendOptionalU64(l, 5, c.Limits.MaxSuspensions)
		b = appendMessage(b, 2, l)
	}
	b = appendBoolField(b, 3, c.TypeCheck)
	if c.TypeCheckStubs != nil {
		b = appendStringAlways(b, 4, *c.TypeCheckStubs)
	}
	b = appendStringField(b, 5, c.MontyVersion)
	if c.AssertMessageAnnotations != nil {
		b = appendVarintField(b, 6, uint64(*c.AssertMessageAnnotations))
	}
	b = appendUintField(b, 7, uint64(c.TypeCheckFormat))
	b = appendBoolField(b, 8, c.TypeCheckColor)
	b = appendUintField(b, 9, uint64(c.ProtocolVersion))
	if c.PrintFlushIntervalMs != nil {
		b = appendVarintField(b, 10, uint64(*c.PrintFlushIntervalMs))
	}
	return b, nil
}

func (r InstallDependencies) appendBody(b []byte) ([]byte, error) {
	for _, req := range r.Requirements {
		b = appendStringAlways(b, 1, req)
	}
	return b, nil
}

func (f Feed) appendBody(b []byte) ([]byte, error) {
	b = appendStringField(b, 1, f.Code)
	for _, in := range f.Inputs {
		nv := appendStringField(nil, 1, in.Name)
		var err error
		if nv, err = appendValueField(nv, 2, in.Value); err != nil {
			return b, err
		}
		b = appendMessage(b, 2, nv)
	}
	b = appendBoolField(b, 3, f.SkipTypeCheck)
	return appendStringField(b, 4, f.Cwd), nil
}

func appendExtResult(b []byte, r ExtResult) ([]byte, error) {
	switch r.Kind {
	case ExtReturn:
		return appendValueField(b, 1, r.Value)
	case ExtError:
		return appendMessage(b, 2, appendExceptionBody(nil, r.Error)), nil
	case ExtFuture:
		return appendVarintField(b, 3, uint64(r.FutureCallID)), nil
	case ExtNotFound:
		return appendStringAlways(b, 4, r.NotFoundName), nil
	case ExtNotHandled:
		return appendMessage(b, 5, nil), nil
	}
	return b, &ConversionError{Message: "external result has no kind"}
}

func (r ResumeCall) appendBody(b []byte) ([]byte, error) {
	b = appendUintField(b, 1, uint64(r.CallID))
	res, err := appendExtResult(nil, r.Result)
	if err != nil {
		return b, err
	}
	return appendMessage(b, 2, res), nil
}

func (r ResumeNameLookup) appendBody(b []byte) ([]byte, error) {
	switch r.Kind {
	case LookupValue:
		return appendValueField(b, 1, r.Value)
	case LookupUndefined:
		return appendMessage(b, 2, nil), nil
	case LookupError:
		return appendMessage(b, 3, appendExceptionBody(nil, r.Error)), nil
	}
	return b, &ConversionError{Message: "name lookup result has no kind"}
}

func (r ResumeFutures) appendBody(b []byte) ([]byte, error) {
	for _, fr := range r.Results {
		body := appendUintField(nil, 1, uint64(fr.CallID))
		res, err := appendExtResult(nil, fr.Result)
		if err != nil {
			return b, err
		}
		body = appendMessage(body, 2, res)
		b = appendMessage(b, 1, body)
	}
	return b, nil
}

func (Dump) appendBody(b []byte) ([]byte, error) { return b, nil }

func (l Load) appendBody(b []byte) ([]byte, error) {
	if len(l.State) == 0 {
		return b, nil
	}
	return appendBytesAlways(b, 1, l.State), nil
}

func (Reset) appendBody(b []byte) ([]byte, error)    { return b, nil }
func (Shutdown) appendBody(b []byte) ([]byte, error) { return b, nil }

func (a AbortFeed) appendBody(b []byte) ([]byte, error) {
	return appendMessage(b, 1, appendExceptionBody(nil, a.Exception)), nil
}

// RequestValues returns the values a request carries, for depth checks.
func RequestValues(req Request) []any {
	switch r := req.(type) {
	case Feed:
		vals := make([]any, 0, len(r.Inputs))
		for _, in := range r.Inputs {
			vals = append(vals, in.Value)
		}
		return vals
	case ResumeCall:
		if r.Result.Kind == ExtReturn {
			return []any{r.Result.Value}
		}
	case ResumeNameLookup:
		if r.Kind == LookupValue {
			return []any{r.Value}
		}
	case ResumeFutures:
		var vals []any
		for _, fr := range r.Results {
			if fr.Result.Kind == ExtReturn {
				vals = append(vals, fr.Result.Value)
			}
		}
		return vals
	}
	return nil
}

var _ = value.Ellipsis
