package wire

import "google.golang.org/protobuf/encoding/protowire"

func appendExceptionBody(b []byte, e *Exception) []byte {
	b = appendStringField(b, 1, e.ExcType)
	if e.Message != nil {
		b = appendStringAlways(b, 2, *e.Message)
	}
	for _, f := range e.Traceback {
		b = appendMessage(b, 3, appendFrameBody(nil, f))
	}
	if e.Data != nil {
		var data []byte
		if u := e.Data.Unicode; u != nil {
			body := appendStringField(nil, 1, u.Encoding)
			if u.ObjectStr != nil {
				body = appendStringAlways(body, 3, *u.ObjectStr)
			} else {
				body = appendBytesAlways(body, 2, u.ObjectBytes)
			}
			body = appendUintField(body, 4, u.Start)
			body = appendUintField(body, 5, u.End)
			body = appendStringField(body, 6, u.Reason)
			data = appendMessage(data, 1, body)
		} else if j := e.Data.JSON; j != nil {
			body := appendStringField(nil, 1, j.Msg)
			if j.Doc != nil {
				body = appendStringAlways(body, 2, *j.Doc)
			}
			body = appendUintField(body, 3, j.Pos)
			body = appendUintField(body, 4, j.Lineno)
			body = appendUintField(body, 5, j.Colno)
			data = appendMessage(data, 2, body)
		}
		b = appendMessage(b, 4, data)
	}
	return b
}

func appendFrameBody(b []byte, f Frame) []byte {
	b = appendStringField(b, 1, f.Filename)
	b = appendMessage(b, 2, appendCodeLoc(nil, f.Start))
	b = appendMessage(b, 3, appendCodeLoc(nil, f.End))
	if f.FrameName != nil {
		b = appendStringAlways(b, 4, *f.FrameName)
	}
	if f.PreviewLine != nil {
		b = appendStringAlways(b, 5, *f.PreviewLine)
	}
	b = appendBoolField(b, 6, f.HideCaret)
	return appendBoolField(b, 7, f.HideFrameName)
}

func appendCodeLoc(b []byte, c CodeLoc) []byte {
	b = appendUintField(b, 1, uint64(c.Line))
	return appendUintField(b, 2, uint64(c.Column))
}

func decodeException(msg []byte) (*Exception, error) {
	r := &reader{b: msg}
	e := &Exception{}
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		switch num {
		case 1:
			e.ExcType = r.str(num, typ)
		case 2:
			m := r.str(num, typ)
			e.Message = &m
		case 3:
			f, err := decodeFrame(r.bytes(num, typ))
			if err != nil {
				return nil, err
			}
			e.Traceback = append(e.Traceback, f)
		case 4:
			d, err := decodeExcData(r.bytes(num, typ))
			if err != nil {
				return nil, err
			}
			e.Data = d
		default:
			r.skip(num, typ)
		}
	}
	return e, r.err
}

func decodeFrame(msg []byte) (Frame, error) {
	r := &reader{b: msg}
	var f Frame
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		switch num {
		case 1:
			f.Filename = r.str(num, typ)
		case 2:
			f.Start = decodeCodeLoc(r, r.bytes(num, typ))
		case 3:
			f.End = decodeCodeLoc(r, r.bytes(num, typ))
		case 4:
			s := r.str(num, typ)
			f.FrameName = &s
		case 5:
			s := r.str(num, typ)
			f.PreviewLine = &s
		case 6:
			f.HideCaret = r.varint(num, typ) != 0
		case 7:
			f.HideFrameName = r.varint(num, typ) != 0
		default:
			r.skip(num, typ)
		}
	}
	return f, r.err
}

func decodeCodeLoc(parent *reader, msg []byte) CodeLoc {
	r := &reader{b: msg}
	var c CodeLoc
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		switch num {
		case 1:
			c.Line = uint32(r.varint(num, typ))
		case 2:
			c.Column = uint32(r.varint(num, typ))
		default:
			r.skip(num, typ)
		}
	}
	if r.err != nil && parent.err == nil {
		parent.err = r.err
	}
	return c
}

func decodeExcData(msg []byte) (*ExcData, error) {
	r := &reader{b: msg}
	d := &ExcData{}
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		switch num {
		case 1:
			u := &UnicodeErrorData{}
			ur := &reader{b: r.bytes(num, typ)}
			for {
				n, t, ok := ur.next()
				if !ok {
					break
				}
				switch n {
				case 1:
					u.Encoding = ur.str(n, t)
				case 2:
					u.ObjectBytes = append([]byte{}, ur.bytes(n, t)...)
				case 3:
					s := ur.str(n, t)
					u.ObjectStr = &s
				case 4:
					u.Start = ur.varint(n, t)
				case 5:
					u.End = ur.varint(n, t)
				case 6:
					u.Reason = ur.str(n, t)
				default:
					ur.skip(n, t)
				}
			}
			if ur.err != nil {
				return nil, ur.err
			}
			d.Unicode = u
		case 2:
			j := &JSONErrorData{}
			jr := &reader{b: r.bytes(num, typ)}
			for {
				n, t, ok := jr.next()
				if !ok {
					break
				}
				switch n {
				case 1:
					j.Msg = jr.str(n, t)
				case 2:
					s := jr.str(n, t)
					j.Doc = &s
				case 3:
					j.Pos = jr.varint(n, t)
				case 4:
					j.Lineno = jr.varint(n, t)
				case 5:
					j.Colno = jr.varint(n, t)
				default:
					jr.skip(n, t)
				}
			}
			if jr.err != nil {
				return nil, jr.err
			}
			d.JSON = j
		default:
			r.skip(num, typ)
		}
	}
	return d, r.err
}

var _ = protowire.BytesType
