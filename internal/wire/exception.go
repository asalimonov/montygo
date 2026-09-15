package wire

import (
	"strconv"
	"strings"
	"unicode"
)

// CodeLoc is a 1-based line/column position.
type CodeLoc struct {
	Line   uint32
	Column uint32
}

// Frame is one traceback frame.
type Frame struct {
	Filename      string
	Start         CodeLoc
	End           CodeLoc
	FrameName     *string
	PreviewLine   *string
	HideCaret     bool
	HideFrameName bool
}

// UnicodeErrorData carries UnicodeDecodeError/UnicodeEncodeError fields.
type UnicodeErrorData struct {
	Encoding    string
	ObjectBytes []byte
	ObjectStr   *string
	Start       uint64
	End         uint64
	Reason      string
}

// JSONErrorData carries json.JSONDecodeError fields.
type JSONErrorData struct {
	Msg    string
	Doc    *string
	Pos    uint64
	Lineno uint64
	Colno  uint64
}

// ExcData is the structured payload of a raised exception.
type ExcData struct {
	Unicode *UnicodeErrorData
	JSON    *JSONErrorData
}

// Exception is a raised Python exception with its traceback.
type Exception struct {
	ExcType   string
	Message   *string
	Traceback []Frame
	Data      *ExcData
}

// NewException builds an exception with a message.
func NewException(excType, message string) *Exception {
	return &Exception{ExcType: excType, Message: &message}
}

// MessageText returns the message, or "" when absent.
func (e *Exception) MessageText() string {
	if e.Message == nil {
		return ""
	}
	return *e.Message
}

// Summary returns "Type: message" or "Type".
func (e *Exception) Summary() string {
	if e.Message == nil {
		return e.ExcType
	}
	return e.ExcType + ": " + *e.Message
}

const repeatFramesShown = 3

// Render formats the exception like monty's MontyException Display.
func (e *Exception) Render() string {
	var b strings.Builder
	if len(e.Traceback) > 0 {
		b.WriteString("Traceback (most recent call last):\n")
	}
	for i := 0; i < len(e.Traceback); {
		frame := e.Traceback[i]
		repeat := 1
		for i+repeat < len(e.Traceback) && framesIdentical(frame, e.Traceback[i+repeat]) {
			repeat++
		}
		if repeat > repeatFramesShown {
			for j := 0; j < repeatFramesShown; j++ {
				writeFrame(&b, e.Traceback[i+j])
			}
			b.WriteString("  [Previous line repeated " + strconv.Itoa(repeat-repeatFramesShown) + " more times]\n")
		} else {
			for j := 0; j < repeat; j++ {
				writeFrame(&b, e.Traceback[i+j])
			}
		}
		i += repeat
	}
	b.WriteString(e.Summary())
	return b.String()
}

func framesIdentical(a, b Frame) bool {
	if a.Filename != b.Filename || a.Start.Line != b.Start.Line {
		return false
	}
	if (a.FrameName == nil) != (b.FrameName == nil) {
		return false
	}
	return a.FrameName == nil || *a.FrameName == *b.FrameName
}

func writeFrame(b *strings.Builder, f Frame) {
	b.WriteString(`  File "` + f.Filename + `", line ` + strconv.FormatUint(uint64(f.Start.Line), 10))
	if !f.HideFrameName {
		b.WriteString(", in ")
		if f.FrameName != nil {
			b.WriteString(*f.FrameName)
		} else {
			b.WriteString("<module>")
		}
	}
	if f.PreviewLine == nil {
		b.WriteByte('\n')
		return
	}
	line := *f.PreviewLine
	if f.Start.Line != f.End.Line {
		b.WriteByte('\n')
		for _, blockLine := range rustLines(line) {
			b.WriteString("    " + blockLine + "\n")
		}
		return
	}
	trimmed := strings.TrimLeftFunc(line, unicode.IsSpace)
	b.WriteString("\n    " + trimmed + "\n")
	if f.HideCaret {
		return
	}
	leading := len(line) - len(trimmed)
	caretStart := 4
	if int(f.Start.Column) > leading {
		caretStart = 4 + int(f.Start.Column) - leading - 1
	}
	caretLen := int(f.End.Column) - int(f.Start.Column)
	if caretLen < 1 {
		caretLen = 1
	}
	b.WriteString(strings.Repeat(" ", caretStart) + strings.Repeat("~", caretLen) + "\n")
}

func rustLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	for i, p := range parts {
		parts[i] = strings.TrimSuffix(p, "\r")
	}
	return parts
}
