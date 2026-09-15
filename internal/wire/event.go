package wire

import (
	"errors"
	"fmt"

	"github.com/asalimonov/montygo/internal/value"
)

// EventKind identifies a ChildEvent arm.
type EventKind uint8

const (
	EventNone EventKind = iota
	EventPrint
	EventFunctionCall
	EventOsCall
	EventNameLookup
	EventResolveFutures
	EventComplete
	EventError
	EventTypingError
	EventDumpResult
	EventOk
	EventFatalError
	EventShutdown
)

var eventNames = [...]string{"None", "Print", "FunctionCall", "OsCall", "NameLookup", "ResolveFutures", "Complete", "Error", "TypingError", "DumpResult", "Ok", "FatalError", "ShutdownDump"}

func (k EventKind) String() string {
	if int(k) < len(eventNames) {
		return eventNames[k]
	}
	return fmt.Sprintf("EventKind(%d)", k)
}

// PrintSegment is one run of output on a single stream (1 stdout, 2 stderr).
type PrintSegment struct {
	Stream uint8
	Text   string
}

// FunctionCall is an external-function or host-method suspension.
type FunctionCall struct {
	FunctionName    string
	Args            []any
	Kwargs          []value.Pair
	CallID          uint32
	ObjectID        string
	AllowEagerAwait bool
}

// NameLookup is an undefined-name or lazy-attribute suspension.
type NameLookup struct {
	Name     string
	ObjectID string
}

// Event is a decoded ChildEvent.
type Event struct {
	Kind            EventKind
	Print           []PrintSegment
	FunctionCall    *FunctionCall
	OsCall          *OsCall
	NameLookup      *NameLookup
	PendingCallIDs  []uint32
	Value           any
	HasValue        bool
	Exception       *Exception
	Diagnostics     string
	State           []byte
	FatalMessage    string
	ShutdownDump    []byte
	HasShutdownDump bool

	TotalExecutionMicros uint64
	MaxDurationMicros    *uint64
	RestoredScriptName   *string
	MaxSuspensions       *uint64
}

// IsSuspension reports whether the event suspends the sandbox.
func (e *Event) IsSuspension() bool {
	switch e.Kind {
	case EventFunctionCall, EventOsCall, EventNameLookup, EventResolveFutures:
		return true
	}
	return false
}

// DecodeEvent decodes a ChildEvent frame under the default decode budget.
func DecodeEvent(payload []byte) (*Event, error) {
	return DecodeEventWithBudget(payload, NewBudget(DefaultMaxDecodeBytes))
}

// DecodeEventWithBudget decodes a ChildEvent frame.
func DecodeEventWithBudget(payload []byte, budget *Budget) (*Event, error) {
	r := &reader{b: payload}
	ev := &Event{}
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		var err error
		switch num {
		case 1:
			ev.Kind = EventPrint
			ev.Print, err = decodePrint(r.bytes(num, typ))
		case 2:
			ev.Kind = EventFunctionCall
			ev.FunctionCall, err = decodeFunctionCall(r.bytes(num, typ), budget)
		case 3:
			ev.Kind = EventOsCall
			ev.OsCall, err = decodeOsCall(r.bytes(num, typ), budget)
		case 4:
			ev.Kind = EventNameLookup
			ev.NameLookup, err = decodeNameLookup(r.bytes(num, typ))
		case 5:
			ev.Kind = EventResolveFutures
			sub := &reader{b: r.bytes(num, typ)}
			ids := []uint32{}
			for {
				n, t, ok := sub.next()
				if !ok {
					break
				}
				if n == 1 {
					ids = sub.uint32s(n, t, ids)
				} else {
					sub.skip(n, t)
				}
			}
			ev.PendingCallIDs, err = ids, sub.err
		case 6:
			ev.Kind = EventComplete
			sub := &reader{b: r.bytes(num, typ)}
			for {
				n, t, ok := sub.next()
				if !ok {
					break
				}
				if n == 1 {
					ev.Value, err = decodeValue(sub.bytes(n, t), budget, 0)
					ev.HasValue = true
					if err != nil {
						break
					}
				} else {
					sub.skip(n, t)
				}
			}
			if err == nil {
				err = sub.err
			}
		case 7:
			ev.Kind = EventError
			sub := &reader{b: r.bytes(num, typ)}
			for {
				n, t, ok := sub.next()
				if !ok {
					break
				}
				if n == 1 {
					ev.Exception, err = decodeException(sub.bytes(n, t))
					if err != nil {
						break
					}
				} else {
					sub.skip(n, t)
				}
			}
			if err == nil {
				err = sub.err
			}
		case 8:
			ev.Kind = EventTypingError
			ev.Diagnostics, err = decodeSingleString(r.bytes(num, typ))
		case 9:
			ev.Kind = EventDumpResult
			sub := &reader{b: r.bytes(num, typ)}
			for {
				n, t, ok := sub.next()
				if !ok {
					break
				}
				if n == 1 {
					ev.State = append([]byte{}, sub.bytes(n, t)...)
				} else {
					sub.skip(n, t)
				}
			}
			err = sub.err
		case 10:
			ev.Kind = EventOk
			r.bytes(num, typ)
		case 11:
			ev.Kind = EventFatalError
			ev.FatalMessage, err = decodeSingleString(r.bytes(num, typ))
		case 12:
			ev.Kind = EventShutdown
			sub := &reader{b: r.bytes(num, typ)}
			for {
				n, t, ok := sub.next()
				if !ok {
					break
				}
				if n == 1 {
					ev.ShutdownDump = append([]byte{}, sub.bytes(n, t)...)
					ev.HasShutdownDump = true
				} else {
					sub.skip(n, t)
				}
			}
			err = sub.err
		case 20:
			ev.TotalExecutionMicros = r.varint(num, typ)
		case 21:
			v := r.varint(num, typ)
			ev.MaxDurationMicros = &v
		case 22:
			s := r.str(num, typ)
			ev.RestoredScriptName = &s
		case 23:
			v := r.varint(num, typ)
			ev.MaxSuspensions = &v
		default:
			r.skip(num, typ)
		}
		if err != nil {
			return nil, err
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	return ev, nil
}

func decodeSingleString(msg []byte) (string, error) {
	r := &reader{b: msg}
	var s string
	for {
		n, t, ok := r.next()
		if !ok {
			break
		}
		if n == 1 {
			s = r.str(n, t)
		} else {
			r.skip(n, t)
		}
	}
	return s, r.err
}

func decodePrint(msg []byte) ([]PrintSegment, error) {
	r := &reader{b: msg}
	var segs []PrintSegment
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		if num != 3 {
			r.skip(num, typ)
			continue
		}
		sub := &reader{b: r.bytes(num, typ)}
		seg := PrintSegment{Stream: 1}
		for {
			n, t, ok := sub.next()
			if !ok {
				break
			}
			switch n {
			case 1:
				if sub.varint(n, t) == 2 {
					seg.Stream = 2
				}
			case 2:
				seg.Text = sub.str(n, t)
			default:
				sub.skip(n, t)
			}
		}
		if sub.err != nil {
			return nil, sub.err
		}
		segs = append(segs, seg)
	}
	return segs, r.err
}

func decodeFunctionCall(msg []byte, budget *Budget) (*FunctionCall, error) {
	r := &reader{b: msg}
	fc := &FunctionCall{Args: []any{}}
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		var err error
		switch num {
		case 1:
			fc.FunctionName = r.str(num, typ)
		case 2:
			var v any
			v, err = decodeValue(r.bytes(num, typ), budget, 0)
			fc.Args = append(fc.Args, v)
		case 3:
			var k, v any
			k, v, err = decodePair(r.bytes(num, typ), budget, 0)
			fc.Kwargs = append(fc.Kwargs, value.Pair{Key: k, Value: v})
		case 4:
			fc.CallID = uint32(r.varint(num, typ))
		case 5:
			fc.ObjectID, err = decodeUUIDMessage(r.bytes(num, typ))
		case 6:
			fc.AllowEagerAwait = r.varint(num, typ) != 0
		default:
			r.skip(num, typ)
		}
		if err != nil {
			return nil, err
		}
	}
	return fc, r.err
}

func decodeNameLookup(msg []byte) (*NameLookup, error) {
	r := &reader{b: msg}
	nl := &NameLookup{}
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		switch num {
		case 1:
			nl.Name = r.str(num, typ)
		case 2:
			id, err := decodeUUIDMessage(r.bytes(num, typ))
			if err != nil {
				return nil, errors.New("NameLookup.object_id is not a 16-byte uuid")
			}
			nl.ObjectID = id
		default:
			r.skip(num, typ)
		}
	}
	return nl, r.err
}
