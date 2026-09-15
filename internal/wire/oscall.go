package wire

import (
	"fmt"

	"github.com/asalimonov/montygo/internal/value"
)

// OsOp identifies an OsCall arm; values are the proto field numbers.
type OsOp uint8

const (
	OpExists       OsOp = 2
	OpIsFile       OsOp = 3
	OpIsDir        OsOp = 4
	OpIsSymlink    OsOp = 5
	OpReadText     OsOp = 6
	OpReadBytes    OsOp = 7
	OpStat         OsOp = 8
	OpIterdir      OsOp = 9
	OpResolve      OsOp = 10
	OpAbsolute     OsOp = 11
	OpUnlink       OsOp = 12
	OpRmdir        OsOp = 13
	OpWriteText    OsOp = 14
	OpAppendText   OsOp = 15
	OpWriteBytes   OsOp = 16
	OpAppendBytes  OsOp = 17
	OpOpen         OsOp = 18
	OpMkdir        OsOp = 19
	OpRename       OsOp = 20
	OpGetenv       OsOp = 21
	OpGetEnviron   OsOp = 22
	OpDateToday    OsOp = 23
	OpDateTimeNow  OsOp = 24
	firstFSOp           = OpExists
	lastFSOp            = OpRename
	lastPathOnlyOp      = OpRmdir
)

var opNames = map[OsOp]string{
	OpExists: "Path.exists", OpIsFile: "Path.is_file", OpIsDir: "Path.is_dir", OpIsSymlink: "Path.is_symlink",
	OpReadText: "Path.read_text", OpReadBytes: "Path.read_bytes", OpStat: "Path.stat", OpIterdir: "Path.iterdir",
	OpResolve: "Path.resolve", OpAbsolute: "Path.absolute", OpUnlink: "Path.unlink", OpRmdir: "Path.rmdir",
	OpWriteText: "Path.write_text", OpAppendText: "Path.append_text", OpWriteBytes: "Path.write_bytes",
	OpAppendBytes: "Path.append_bytes", OpOpen: "open", OpMkdir: "Path.mkdir", OpRename: "Path.rename",
	OpGetenv: "os.getenv", OpGetEnviron: "os.environ", OpDateToday: "date.today", OpDateTimeNow: "datetime.now",
}

// Name returns the Python-level function name of the operation.
func (o OsOp) Name() string { return opNames[o] }

// OsCall is a decoded OS-call suspension.
type OsCall struct {
	CallID  uint32
	Op      OsOp
	Path    string
	Dst     string
	Text    string
	Data    []byte
	Mode    string
	Parents bool
	ExistOK bool
	Key     string
	Default any
	HasTZ   bool
	TZ      value.TimeZone
	// PayloadErr is set when the call arm decoded but its content is invalid.
	PayloadErr error
}

// Name returns the Python-level function name.
func (c *OsCall) Name() string { return c.Op.Name() }

// IsFS reports whether the call has a filesystem path.
func (c *OsCall) IsFS() bool { return c.Op >= firstFSOp && c.Op <= lastFSOp }

// IsExistenceCheck reports exists/is_file/is_dir/is_symlink.
func (c *OsCall) IsExistenceCheck() bool { return c.Op >= OpExists && c.Op <= OpIsSymlink }

// IsWrite reports whether the call mutates the filesystem.
func (c *OsCall) IsWrite() bool {
	switch c.Op {
	case OpWriteText, OpWriteBytes, OpAppendText, OpAppendBytes, OpMkdir, OpUnlink, OpRmdir, OpRename:
		return true
	case OpOpen:
		return len(c.Mode) > 0 && (c.Mode[0] == 'w' || c.Mode[0] == 'a')
	}
	return false
}

// NullMessage returns the message raised for an embedded NUL in the path.
func (c *OsCall) NullMessage(dst bool) string {
	switch c.Op {
	case OpMkdir:
		return "mkdir: embedded null character in path"
	case OpUnlink:
		return "unlink: embedded null character in path"
	case OpRmdir:
		return "rmdir: embedded null character in path"
	case OpStat:
		return "stat: embedded null character in path"
	case OpIterdir:
		return "scandir: embedded null character in path"
	case OpResolve:
		return "lstat: embedded null character in path"
	case OpRename:
		if dst {
			return "rename: embedded null character in dst"
		}
		return "rename: embedded null character in src"
	}
	return "embedded null byte"
}

func argPath(p string) value.Path {
	if p == "" {
		return value.Path("")
	}
	return value.Path(value.NormalizeVirtualPath(p))
}

// Args returns the positional and keyword arguments presented to an OS handler.
func (c *OsCall) Args() ([]any, []value.Pair) {
	switch {
	case c.Op >= OpExists && c.Op <= lastPathOnlyOp:
		return []any{argPath(c.Path)}, nil
	}
	switch c.Op {
	case OpWriteText, OpAppendText:
		return []any{argPath(c.Path), c.Text}, nil
	case OpWriteBytes, OpAppendBytes:
		return []any{argPath(c.Path), append([]byte{}, c.Data...)}, nil
	case OpOpen:
		return []any{argPath(c.Path), c.Mode}, nil
	case OpMkdir:
		return []any{argPath(c.Path)}, []value.Pair{{Key: "parents", Value: c.Parents}, {Key: "exist_ok", Value: c.ExistOK}}
	case OpRename:
		return []any{argPath(c.Path), argPath(c.Dst)}, nil
	case OpGetenv:
		return []any{c.Key, c.Default}, nil
	case OpGetEnviron, OpDateToday:
		return []any{}, nil
	case OpDateTimeNow:
		if c.HasTZ {
			return []any{c.TZ}, nil
		}
		return []any{nil}, nil
	}
	return nil, nil
}

func decodeOsCall(msg []byte, budget *Budget) (*OsCall, error) {
	r := &reader{b: msg}
	c := &OsCall{}
	for {
		num, typ, ok := r.next()
		if !ok {
			break
		}
		switch {
		case num == 1:
			c.CallID = uint32(r.varint(num, typ))
		case num >= 2 && num <= 13:
			c.Op = OsOp(num)
			c.Path = r.str(num, typ)
		case num == 14 || num == 15 || num == 16 || num == 17:
			c.Op = OsOp(num)
			sub := &reader{b: r.bytes(num, typ)}
			for {
				n, t, ok := sub.next()
				if !ok {
					break
				}
				switch n {
				case 1:
					c.Path = sub.str(n, t)
				case 2:
					data := sub.bytes(n, t)
					if err := budget.charge(len(data)); err != nil {
						return nil, err
					}
					if num <= 15 {
						c.Text = string(data)
					} else {
						c.Data = append([]byte{}, data...)
					}
				default:
					sub.skip(n, t)
				}
			}
			if sub.err != nil {
				return nil, sub.err
			}
		case num == 18:
			c.Op = OpOpen
			sub := &reader{b: r.bytes(num, typ)}
			for {
				n, t, ok := sub.next()
				if !ok {
					break
				}
				switch n {
				case 1:
					c.Path = sub.str(n, t)
				case 2:
					c.Mode = sub.str(n, t)
				default:
					sub.skip(n, t)
				}
			}
			if sub.err != nil {
				return nil, sub.err
			}
			if !validHandleMode(c.Mode) {
				c.PayloadErr = fmt.Errorf("invalid open mode %q", c.Mode)
			}
		case num == 19:
			c.Op = OpMkdir
			sub := &reader{b: r.bytes(num, typ)}
			for {
				n, t, ok := sub.next()
				if !ok {
					break
				}
				switch n {
				case 1:
					c.Path = sub.str(n, t)
				case 2:
					c.Parents = sub.varint(n, t) != 0
				case 3:
					c.ExistOK = sub.varint(n, t) != 0
				default:
					sub.skip(n, t)
				}
			}
			if sub.err != nil {
				return nil, sub.err
			}
		case num == 20:
			c.Op = OpRename
			sub := &reader{b: r.bytes(num, typ)}
			for {
				n, t, ok := sub.next()
				if !ok {
					break
				}
				switch n {
				case 1:
					c.Path = sub.str(n, t)
				case 2:
					c.Dst = sub.str(n, t)
				default:
					sub.skip(n, t)
				}
			}
			if sub.err != nil {
				return nil, sub.err
			}
		case num == 21:
			c.Op = OpGetenv
			sub := &reader{b: r.bytes(num, typ)}
			for {
				n, t, ok := sub.next()
				if !ok {
					break
				}
				switch n {
				case 1:
					c.Key = sub.str(n, t)
				case 2:
					v, err := decodeValue(sub.bytes(n, t), budget, 1)
					if err != nil {
						return nil, err
					}
					c.Default = v
				default:
					sub.skip(n, t)
				}
			}
			if sub.err != nil {
				return nil, sub.err
			}
		case num == 22 || num == 23:
			c.Op = OsOp(num)
			r.bytes(num, typ)
		case num == 24:
			c.Op = OpDateTimeNow
			sub := &reader{b: r.bytes(num, typ)}
			for {
				n, t, ok := sub.next()
				if !ok {
					break
				}
				if n == 1 {
					tz, err := decodeTimeZone(sub.bytes(n, t))
					if err != nil {
						return nil, err
					}
					c.TZ = tz
					c.HasTZ = true
				} else {
					sub.skip(n, t)
				}
			}
			if sub.err != nil {
				return nil, sub.err
			}
		default:
			r.skip(num, typ)
		}
	}
	return c, r.err
}
