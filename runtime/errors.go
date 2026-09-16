package runtime

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/asalimonov/montygo/internal/wire"
)

// DisplayFormat selects how an error renders.
type DisplayFormat string

const (
	DisplayTraceback DisplayFormat = "traceback"
	DisplayTypeMsg   DisplayFormat = "type-msg"
	DisplayMsg       DisplayFormat = "msg"
)

// ExceptionInfo is the Python exception an error represents.
type ExceptionInfo struct {
	TypeName string
	Message  string
}

// Error is implemented by every error raised by sandboxed code or a worker.
type Error interface {
	error
	Exception() ExceptionInfo
	Display(format DisplayFormat) string
}

// Frame is one structured traceback frame, outermost first.
type Frame struct {
	Filename     string
	Line         int
	Column       int
	EndLine      int
	EndColumn    int
	FunctionName string
	SourceLine   string
}

func typeMsg(typeName, message string) string {
	if message == "" {
		return typeName
	}
	return typeName + ": " + message
}

// RuntimeError is a Python exception raised by the sandboxed code.
type RuntimeError struct {
	lost      bool
	TypeName  string
	Message   string
	Frames    []Frame
	Traceback string
}

func (e *RuntimeError) Is(target error) bool { return e.lost && target == ErrSessionLost }

// MarkSessionLost records that the worker died with this exception, so the
// error also matches ErrSessionLost.
func (e *RuntimeError) MarkSessionLost() { e.lost = true }

func (e *RuntimeError) Error() string            { return typeMsg(e.TypeName, e.Message) }
func (e *RuntimeError) Exception() ExceptionInfo { return ExceptionInfo{e.TypeName, e.Message} }

// Display renders the error; the empty format means traceback.
func (e *RuntimeError) Display(format DisplayFormat) string {
	switch format {
	case DisplayTypeMsg:
		return typeMsg(e.TypeName, e.Message)
	case DisplayMsg:
		return e.Message
	}
	if e.Traceback != "" {
		return e.Traceback
	}
	return renderFallbackTraceback(e.Frames, typeMsg(e.TypeName, e.Message))
}

// TracebackFrames returns the structured frames.
func (e *RuntimeError) TracebackFrames() []Frame { return e.Frames }

// SyntaxError is raised when a snippet does not parse.
type SyntaxError struct {
	Message   string
	Traceback string
}

func (e *SyntaxError) Error() string            { return typeMsg("SyntaxError", e.Message) }
func (e *SyntaxError) Exception() ExceptionInfo { return ExceptionInfo{"SyntaxError", e.Message} }

// Display renders the error; the empty format means msg.
func (e *SyntaxError) Display(format DisplayFormat) string {
	switch format {
	case DisplayTraceback:
		if e.Traceback != "" {
			return e.Traceback
		}
		return typeMsg("SyntaxError", e.Message)
	case DisplayTypeMsg:
		return typeMsg("SyntaxError", e.Message)
	}
	return e.Message
}

// TypingError is raised when type checking rejects a snippet; it did not run.
type TypingError struct {
	Diagnostics string
}

func (e *TypingError) firstLine() string {
	line, _, _ := strings.Cut(e.Diagnostics, "\n")
	return line
}

func (e *TypingError) Error() string            { return typeMsg("TypeError", e.firstLine()) }
func (e *TypingError) Exception() ExceptionInfo { return ExceptionInfo{"TypeError", e.firstLine()} }

// Display returns the rendered diagnostics whatever the format.
func (e *TypingError) Display(DisplayFormat) string { return e.Diagnostics }

// CrashedError reports a worker that died; the session is lost, the pool recovers.
type CrashedError struct {
	Message    string
	TimedOut   bool
	ExitStatus string
}

func (e *CrashedError) Error() string            { return typeMsg("RuntimeError", e.Message) }
func (e *CrashedError) Exception() ExceptionInfo { return ExceptionInfo{"RuntimeError", e.Message} }
func (e *CrashedError) Is(target error) bool     { return target == ErrSessionLost }

func (e *CrashedError) Display(format DisplayFormat) string {
	if format == DisplayTypeMsg || format == DisplayTraceback {
		return typeMsg("RuntimeError", e.Message)
	}
	return e.Message
}

// DisconnectError reports a remote worker connection that closed mid-session.
type DisconnectError struct {
	Message string
	// Code and Reason carry the server's close frame; Code is 0 when the connection dropped without one.
	Code   int
	Reason string
}

func (e *DisconnectError) Error() string            { return typeMsg("RuntimeError", e.Message) }
func (e *DisconnectError) Exception() ExceptionInfo { return ExceptionInfo{"RuntimeError", e.Message} }
func (e *DisconnectError) Is(target error) bool     { return target == ErrSessionLost }

func (e *DisconnectError) Display(format DisplayFormat) string {
	if format == DisplayMsg {
		return e.Message
	}
	return typeMsg("RuntimeError", e.Message)
}

// ShutdownError reports a draining server that did not run the request.
type ShutdownError struct {
	Message string
	Dump    []byte
}

func (e *ShutdownError) Error() string            { return typeMsg("RuntimeError", e.Message) }
func (e *ShutdownError) Exception() ExceptionInfo { return ExceptionInfo{"RuntimeError", e.Message} }
func (e *ShutdownError) Is(target error) bool     { return target == ErrSessionLost }

func (e *ShutdownError) Display(format DisplayFormat) string {
	if format == DisplayMsg {
		return e.Message
	}
	return typeMsg("RuntimeError", e.Message)
}

// RotationError reports a session that could not be moved to a fresh connection
// before its server closed the old one. Dump carries the state captured just
// before the failure, for LoadSession on a new session, or nil when the dump
// itself failed.
type RotationError struct {
	Message string
	Dump    []byte
	Cause   error
}

func (e *RotationError) Error() string            { return typeMsg("RuntimeError", e.Message) }
func (e *RotationError) Exception() ExceptionInfo { return ExceptionInfo{"RuntimeError", e.Message} }
func (e *RotationError) Is(target error) bool     { return target == ErrSessionLost }
func (e *RotationError) Unwrap() error            { return e.Cause }

func (e *RotationError) Display(format DisplayFormat) string {
	if format == DisplayMsg {
		return e.Message
	}
	return typeMsg("RuntimeError", e.Message)
}

// ProtocolError reports a protocol violation or misuse; it poisons the session.
type ProtocolError struct {
	Message string
	cause   error
}

func (e *ProtocolError) Error() string        { return e.Message }
func (e *ProtocolError) Unwrap() error        { return e.cause }
func (e *ProtocolError) Is(target error) bool { return target == ErrSessionLost }

// NewProtocolError reports a protocol violation or misuse wrapping cause.
func NewProtocolError(message string, cause error) *ProtocolError {
	return &ProtocolError{Message: message, cause: cause}
}

// ResourceError reports a host-side limit the sandbox reached; it raises a
// RuntimeError inside the sandbox and leaves the session usable.
type ResourceError struct {
	Resource string
	Limit    uint64
}

func (e *ResourceError) Error() string {
	return fmt.Sprintf("%s limit %d exceeded", e.Resource, e.Limit)
}
func (e *ResourceError) Exception() ExceptionInfo {
	return ExceptionInfo{"RuntimeError", e.Error()}
}
func (e *ResourceError) Display(DisplayFormat) string { return typeMsg("RuntimeError", e.Error()) }

// sessionClosedError distinguishes deliberate closure from unexpected loss.
type sessionClosedError struct{}

func (sessionClosedError) Error() string { return "the session is closed — check out a new one" }

var (
	ErrSessionBusy   = errors.New("session already has an active operation")
	ErrSnapshotStale = errors.New("snapshot does not own the current suspension")
)

// SessionKilledError reports forced termination after an interrupt's grace.
type SessionKilledError struct{ Reason error }

func (e *SessionKilledError) Error() string        { return "session killed to interrupt execution" }
func (e *SessionKilledError) Unwrap() error        { return e.Reason }
func (e *SessionKilledError) Is(target error) bool { return target == ErrSessionLost }

// ConversionError reports a host value that cannot cross into the sandbox.
type ConversionError struct {
	Message string
}

func (e *ConversionError) Error() string { return e.Message }

// OptionError reports invalid options.
type OptionError struct {
	Message string
}

func (e *OptionError) Error() string { return e.Message }

// ValueError reports an invalid argument to a constructor.
type ValueError struct {
	Message string
}

func (e *ValueError) Error() string { return e.Message }

// RaisedError is returned by host code to raise a specific Python exception.
type RaisedError struct {
	ExcType string
	Message string
}

func (e *RaisedError) Error() string { return typeMsg(e.ExcType, e.Message) }

// Raise builds a RaisedError.
func Raise(excType, message string) error { return &RaisedError{ExcType: excType, Message: message} }

var (
	ErrPoolClosed = errors.New("the pool is closed — create a new Monty pool")
	// ErrSessionClosed is returned by every call on a closed session.
	ErrSessionClosed error = sessionClosedError{}
	// ErrSessionLost matches unexpected terminal failures: a crash, timeout,
	// lost connection, server shutdown, protocol violation or forced interrupt.
	// Deliberate ErrSessionClosed does not match it.
	ErrSessionLost     = errors.New("montygo: session lost")
	ErrNotFresh        = errors.New("loadSession / loadSnapshot is only valid on a fresh session, before any feedRun / feedStart / loadSession / loadSnapshot")
	ErrDumpIsSuspended = errors.New("this dump is a suspended snapshot — use loadSnapshot() to resume it")
	ErrDumpIsIdle      = errors.New("this dump is an idle session — use loadSession() to restore it")
	ErrSnapshotResumed = errors.New("snapshot has already been resumed")
	ErrNotOSCall       = errors.New("resumeNotHandled is only valid for OS-call snapshots")
	ErrCheckoutTimeout = errors.New("no monty worker became available within the checkout timeout")
	// ErrTurnCancelled matches the error of every call after a turn was cancelled mid-flight.
	ErrTurnCancelled    = errors.New("a previous protocol turn was cancelled mid-flight; the worker was discarded")
	ErrTelemetryPresent = errors.New("Monty telemetry is already configured")
)

var knownExceptions = map[string]struct{}{}

// KnownExceptionNames lists the Python exception types host errors may raise by name, sorted.
func KnownExceptionNames() []string {
	names := make([]string, 0, len(knownExceptions))
	for name := range knownExceptions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

var excParents = map[string]string{
	"BaseException": "", "SystemExit": "BaseException", "KeyboardInterrupt": "BaseException", "Exception": "BaseException",
	"ArithmeticError": "Exception", "OverflowError": "ArithmeticError", "ZeroDivisionError": "ArithmeticError",
	"LookupError": "Exception", "IndexError": "LookupError", "KeyError": "LookupError",
	"RuntimeError": "Exception", "NotImplementedError": "RuntimeError", "RecursionError": "RuntimeError",
	"AttributeError": "Exception", "FrozenInstanceError": "AttributeError",
	"NameError": "Exception", "UnboundLocalError": "NameError",
	"ValueError": "Exception", "UnicodeDecodeError": "ValueError", "UnicodeEncodeError": "ValueError",
	"json.JSONDecodeError": "ValueError", "binascii.Error": "ValueError", "binascii.Incomplete": "Exception",
	"ImportError": "Exception", "ModuleNotFoundError": "ImportError",
	"OSError": "Exception", "FileNotFoundError": "OSError", "FileExistsError": "OSError", "IsADirectoryError": "OSError",
	"NotADirectoryError": "OSError", "PermissionError": "OSError", "TimeoutError": "OSError",
	"io.UnsupportedOperation": "OSError", "AssertionError": "Exception", "MemoryError": "Exception",
	"StopIteration": "Exception", "SyntaxError": "Exception", "TypeError": "Exception", "re.PatternError": "Exception",
}

func init() {
	for name := range excParents {
		knownExceptions[name] = struct{}{}
	}
}

// IsSubclass reports whether excType is base or derives from it.
func IsSubclass(excType, base string) bool {
	if excType == base || base == "BaseException" {
		_, ok := excParents[excType]
		return ok
	}
	if excType == "io.UnsupportedOperation" && base == "ValueError" {
		return true
	}
	for t := excParents[excType]; t != ""; t = excParents[t] {
		if t == base {
			return true
		}
	}
	return false
}

func renderFallbackTraceback(frames []Frame, summary string) string {
	var b strings.Builder
	b.WriteString("Traceback (most recent call last):\n")
	for _, f := range frames {
		name := f.FunctionName
		if name == "" {
			name = "<module>"
		}
		b.WriteString(`  File "` + f.Filename + `", line ` + itoa(f.Line) + ", in " + name + "\n")
		if f.SourceLine != "" {
			trimmed := strings.TrimLeft(f.SourceLine, " \t")
			b.WriteString("    " + trimmed + "\n")
			if !strings.HasPrefix(trimmed, "raise") && f.Column > 0 && f.EndColumn > f.Column {
				indent := len(f.SourceLine) - len(trimmed)
				b.WriteString(strings.Repeat(" ", 4+f.Column-1-indent) + strings.Repeat("~", f.EndColumn-f.Column) + "\n")
			}
		}
	}
	b.WriteString(summary)
	return b.String()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func ErrorFromException(exc *wire.Exception) error {
	if exc.ExcType == "SyntaxError" {
		return &SyntaxError{Message: exc.MessageText(), Traceback: exc.Render()}
	}
	frames := make([]Frame, 0, len(exc.Traceback))
	for _, f := range exc.Traceback {
		fr := Frame{Filename: f.Filename, Line: int(f.Start.Line), Column: int(f.Start.Column), EndLine: int(f.End.Line), EndColumn: int(f.End.Column)}
		if f.FrameName != nil {
			fr.FunctionName = *f.FrameName
		}
		if f.PreviewLine != nil {
			fr.SourceLine = *f.PreviewLine
		}
		frames = append(frames, fr)
	}
	return &RuntimeError{TypeName: exc.ExcType, Message: exc.MessageText(), Frames: frames, Traceback: exc.Render()}
}

// ExceptionParts maps a host error to the Python exception the sandbox raises.
func ExceptionParts(err error) (string, string) {
	var raised *RaisedError
	if errors.As(err, &raised) {
		if _, ok := knownExceptions[raised.ExcType]; ok {
			return raised.ExcType, raised.Message
		}
		return "RuntimeError", raised.Message
	}
	var me Error
	if errors.As(err, &me) {
		info := me.Exception()
		return info.TypeName, info.Message
	}
	var conv *ConversionError
	if errors.As(err, &conv) {
		return "TypeError", conv.Message
	}
	return "RuntimeError", err.Error()
}
