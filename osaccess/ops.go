// Package osaccess answers Monty OS calls from Go: the Ops dispatch contract,
// stat helpers and OSAccess, an in-memory virtual filesystem.
package osaccess

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	monty "github.com/asalimonov/montygo"
)

// Ops is the set of OS operations a Monty OS handler can answer.
type Ops interface {
	PathExists(path monty.Path) (bool, error)
	PathIsFile(path monty.Path) (bool, error)
	PathIsDir(path monty.Path) (bool, error)
	PathIsSymlink(path monty.Path) (bool, error)
	PathOpen(path monty.Path, mode string) (*monty.FileHandle, error)
	PathReadText(path monty.Path) (string, error)
	PathReadBytes(path monty.Path) ([]byte, error)
	PathWriteText(path monty.Path, data string) (int, error)
	PathWriteBytes(path monty.Path, data []byte) (int, error)
	PathAppendText(path monty.Path, data string) (int, error)
	PathAppendBytes(path monty.Path, data []byte) (int, error)
	PathMkdir(path monty.Path, parents, existOK bool) error
	PathUnlink(path monty.Path) error
	PathRmdir(path monty.Path) error
	PathIterdir(path monty.Path) ([]monty.Path, error)
	PathStat(path monty.Path) (StatResult, error)
	PathRename(path, target monty.Path) error
	PathResolve(path monty.Path) (string, error)
	PathAbsolute(path monty.Path) (string, error)
	Getenv(key string, def any) (any, error)
	GetEnviron() (map[string]string, error)
	DateToday() (monty.Date, error)
	DatetimeNow(tz *monty.TimeZone) (monty.DateTime, error)
}

// ErrNotImplemented declines an operation; the dispatcher answers NotHandled.
var ErrNotImplemented = errors.New("not implemented")

// Base declines every operation except DateToday and DatetimeNow, which use the host clock.
type Base struct{}

func (Base) PathExists(monty.Path) (bool, error)    { return false, ErrNotImplemented }
func (Base) PathIsFile(monty.Path) (bool, error)    { return false, ErrNotImplemented }
func (Base) PathIsDir(monty.Path) (bool, error)     { return false, ErrNotImplemented }
func (Base) PathIsSymlink(monty.Path) (bool, error) { return false, ErrNotImplemented }
func (Base) PathOpen(monty.Path, string) (*monty.FileHandle, error) {
	return nil, ErrNotImplemented
}
func (Base) PathReadText(monty.Path) (string, error)         { return "", ErrNotImplemented }
func (Base) PathReadBytes(monty.Path) ([]byte, error)        { return nil, ErrNotImplemented }
func (Base) PathWriteText(monty.Path, string) (int, error)   { return 0, ErrNotImplemented }
func (Base) PathWriteBytes(monty.Path, []byte) (int, error)  { return 0, ErrNotImplemented }
func (Base) PathAppendText(monty.Path, string) (int, error)  { return 0, ErrNotImplemented }
func (Base) PathAppendBytes(monty.Path, []byte) (int, error) { return 0, ErrNotImplemented }
func (Base) PathMkdir(monty.Path, bool, bool) error          { return ErrNotImplemented }
func (Base) PathUnlink(monty.Path) error                     { return ErrNotImplemented }
func (Base) PathRmdir(monty.Path) error                      { return ErrNotImplemented }
func (Base) PathIterdir(monty.Path) ([]monty.Path, error)    { return nil, ErrNotImplemented }
func (Base) PathStat(monty.Path) (StatResult, error)         { return StatResult{}, ErrNotImplemented }
func (Base) PathRename(monty.Path, monty.Path) error         { return ErrNotImplemented }
func (Base) PathResolve(monty.Path) (string, error)          { return "", ErrNotImplemented }
func (Base) PathAbsolute(monty.Path) (string, error)         { return "", ErrNotImplemented }
func (Base) Getenv(string, any) (any, error)                 { return nil, ErrNotImplemented }
func (Base) GetEnviron() (map[string]string, error)          { return nil, ErrNotImplemented }

// DateToday returns the host's local date.
func (Base) DateToday() (monty.Date, error) {
	y, m, d := time.Now().Date()
	return monty.Date{Year: int32(y), Month: uint8(m), Day: uint8(d)}, nil
}

// DatetimeNow returns the host's local wall clock when tz is nil, else the current time in tz.
func (Base) DatetimeNow(tz *monty.TimeZone) (monty.DateTime, error) {
	now := time.Now()
	if tz == nil {
		dt := monty.DateTimeFromTime(now)
		dt.OffsetSeconds, dt.TimezoneName = nil, nil
		return dt, nil
	}
	dt := monty.DateTimeFromTime(now.In(time.FixedZone("", int(tz.OffsetSeconds))))
	off := tz.OffsetSeconds
	dt.OffsetSeconds = &off
	dt.TimezoneName = nil
	if tz.Name != nil {
		name := *tz.Name
		dt.TimezoneName = &name
	}
	return dt, nil
}

// Handler adapts ops to a monty.OSHandler.
func Handler(ops Ops) monty.OSHandler {
	return func(ctx context.Context, name string, args []any, kwargs monty.Kwargs) (any, error) {
		return Dispatch(ctx, ops, name, args, kwargs)
	}
}

// Dispatch routes one OS call to ops. Unknown names and ErrNotImplemented yield monty.NotHandled.
func Dispatch(_ context.Context, ops Ops, name string, args []any, kwargs monty.Kwargs) (any, error) {
	result, err := dispatch(ops, name, args, kwargs)
	if errors.Is(err, ErrNotImplemented) {
		return monty.NotHandled, nil
	}
	return result, err
}

func dispatch(ops Ops, name string, args []any, kwargs monty.Kwargs) (any, error) {
	switch name {
	case "Path.exists":
		return pathCall(args, "path_exists", ops.PathExists)
	case "Path.is_file":
		return pathCall(args, "path_is_file", ops.PathIsFile)
	case "Path.is_dir":
		return pathCall(args, "path_is_dir", ops.PathIsDir)
	case "Path.is_symlink":
		return pathCall(args, "path_is_symlink", ops.PathIsSymlink)
	case "open":
		if err := arity("path_open", args, 2, 2); err != nil {
			return nil, err
		}
		p, err := pathArg("path_open", args[0])
		if err != nil {
			return nil, err
		}
		mode, err := stringArg("path_open", args[1])
		if err != nil {
			return nil, err
		}
		h, err := ops.PathOpen(p, mode)
		if err != nil {
			return nil, err
		}
		return h, nil
	case "Path.read_text":
		return pathCall(args, "path_read_text", ops.PathReadText)
	case "Path.read_bytes":
		return pathCall(args, "path_read_bytes", ops.PathReadBytes)
	case "Path.write_text":
		return textCall(args, "path_write_text", ops.PathWriteText)
	case "Path.write_bytes":
		return bytesCall(args, "path_write_bytes", ops.PathWriteBytes)
	case "Path.append_text":
		return textCall(args, "path_append_text", ops.PathAppendText)
	case "Path.append_bytes":
		return bytesCall(args, "path_append_bytes", ops.PathAppendBytes)
	case "Path.mkdir":
		if len(kwargs) > 2 {
			return nil, monty.Raise("AssertionError", "Unexpected keyword arguments: "+kwargsRepr(kwargs))
		}
		if err := arity("path_mkdir", args, 1, 1); err != nil {
			return nil, err
		}
		p, err := pathArg("path_mkdir", args[0])
		if err != nil {
			return nil, err
		}
		return nil, ops.PathMkdir(p, truthy(kwargs["parents"]), truthy(kwargs["exist_ok"]))
	case "Path.unlink":
		return unitCall(args, "path_unlink", ops.PathUnlink)
	case "Path.rmdir":
		return unitCall(args, "path_rmdir", ops.PathRmdir)
	case "Path.iterdir":
		paths, err := pathCall(args, "path_iterdir", ops.PathIterdir)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(paths))
		for i, p := range paths {
			out[i] = p
		}
		return out, nil
	case "Path.stat":
		st, err := pathCall(args, "path_stat", ops.PathStat)
		if err != nil {
			return nil, err
		}
		return st.NamedTuple(), nil
	case "Path.rename":
		if err := arity("path_rename", args, 2, 2); err != nil {
			return nil, err
		}
		p, err := pathArg("path_rename", args[0])
		if err != nil {
			return nil, err
		}
		target, err := pathArg("path_rename", args[1])
		if err != nil {
			return nil, err
		}
		return nil, ops.PathRename(p, target)
	case "Path.resolve":
		return pathCall(args, "path_resolve", ops.PathResolve)
	case "Path.absolute":
		return pathCall(args, "path_absolute", ops.PathAbsolute)
	case "os.getenv":
		if err := arity("getenv", args, 1, 2); err != nil {
			return nil, err
		}
		key, err := stringArg("getenv", args[0])
		if err != nil {
			return nil, err
		}
		var def any
		if len(args) == 2 {
			def = args[1]
		}
		return ops.Getenv(key, def)
	case "os.environ":
		if err := arity("get_environ", args, 0, 0); err != nil {
			return nil, err
		}
		env, err := ops.GetEnviron()
		if err != nil {
			return nil, err
		}
		return environDict(env), nil
	case "date.today":
		if err := arity("date_today", args, 0, 0); err != nil {
			return nil, err
		}
		return ops.DateToday()
	case "datetime.now":
		if err := arity("datetime_now", args, 0, 1); err != nil {
			return nil, err
		}
		var tz *monty.TimeZone
		if len(args) == 1 {
			switch x := args[0].(type) {
			case nil:
			case monty.TimeZone:
				tz = &x
			case *monty.TimeZone:
				tz = x
			default:
				return nil, monty.Raise("TypeError", fmt.Sprintf("datetime_now() argument must be a timezone or None, got %T", x))
			}
		}
		return ops.DatetimeNow(tz)
	}
	return nil, ErrNotImplemented
}

func pathCall[T any](args []any, method string, fn func(monty.Path) (T, error)) (T, error) {
	var zero T
	if err := arity(method, args, 1, 1); err != nil {
		return zero, err
	}
	p, err := pathArg(method, args[0])
	if err != nil {
		return zero, err
	}
	return fn(p)
}

func unitCall(args []any, method string, fn func(monty.Path) error) (any, error) {
	if err := arity(method, args, 1, 1); err != nil {
		return nil, err
	}
	p, err := pathArg(method, args[0])
	if err != nil {
		return nil, err
	}
	return nil, fn(p)
}

func textCall(args []any, method string, fn func(monty.Path, string) (int, error)) (any, error) {
	if err := arity(method, args, 2, 2); err != nil {
		return nil, err
	}
	p, err := pathArg(method, args[0])
	if err != nil {
		return nil, err
	}
	data, err := stringArg(method, args[1])
	if err != nil {
		return nil, err
	}
	n, err := fn(p, data)
	if err != nil {
		return nil, err
	}
	return n, nil
}

func bytesCall(args []any, method string, fn func(monty.Path, []byte) (int, error)) (any, error) {
	if err := arity(method, args, 2, 2); err != nil {
		return nil, err
	}
	p, err := pathArg(method, args[0])
	if err != nil {
		return nil, err
	}
	data, ok := args[1].([]byte)
	if !ok {
		return nil, monty.Raise("TypeError", fmt.Sprintf("%s() data must be bytes, got %T", method, args[1]))
	}
	n, err := fn(p, data)
	if err != nil {
		return nil, err
	}
	return n, nil
}

func arity(method string, args []any, lo, hi int) error {
	if len(args) >= lo && len(args) <= hi {
		return nil
	}
	if len(args) < lo {
		return monty.Raise("TypeError", fmt.Sprintf("%s() missing %d required positional argument(s)", method, lo-len(args)))
	}
	return monty.Raise("TypeError", fmt.Sprintf("%s() takes %d positional argument(s) but %d were given", method, hi, len(args)))
}

func pathArg(method string, v any) (monty.Path, error) {
	switch x := v.(type) {
	case monty.Path:
		return x, nil
	case string:
		return monty.Path(x), nil
	case *monty.FileHandle:
		if x != nil {
			return monty.Path(x.Path), nil
		}
	case monty.FileHandle:
		return monty.Path(x.Path), nil
	}
	return "", monty.Raise("TypeError", fmt.Sprintf("%s() expected a path, got %T", method, v))
}

func stringArg(method string, v any) (string, error) {
	if s, ok := v.(string); ok {
		return s, nil
	}
	return "", monty.Raise("TypeError", fmt.Sprintf("%s() expected str, got %T", method, v))
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case int64:
		return x != 0
	case int:
		return x != 0
	case float64:
		return x != 0
	case string:
		return x != ""
	}
	return true
}

func kwargsRepr(kwargs monty.Kwargs) string {
	keys := make([]string, 0, len(kwargs))
	for k := range kwargs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	d := monty.NewDict()
	for _, k := range keys {
		d.Append(k, kwargs[k])
	}
	return monty.Repr(d)
}

func environDict(env map[string]string) *monty.Dict {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	d := monty.NewDict()
	for _, k := range keys {
		d.Append(k, env[k])
	}
	return d
}

// StatResult mirrors os.stat_result.
type StatResult struct {
	StMode, StIno, StDev, StNlink, StUID, StGID, StSize int64
	StAtime, StMtime, StCtime                           float64
}

var statFields = []string{"st_mode", "st_ino", "st_dev", "st_nlink", "st_uid", "st_gid", "st_size", "st_atime", "st_mtime", "st_ctime"}

// FileStat builds a regular-file stat result; mode 0 means 0o644 and mtime nil means now.
func FileStat(size int64, mode int64, mtime *float64) StatResult {
	if mode == 0 {
		mode = 0o644
	}
	return fileStat(size, mode, mtime)
}

// DirStat builds a directory stat result; mode 0 means 0o755 and mtime nil means now.
func DirStat(mode int64, mtime *float64) StatResult {
	if mode == 0 {
		mode = 0o755
	}
	if mode < 0o1000 {
		mode |= 0o040000
	}
	t := statTime(mtime)
	return StatResult{StMode: mode, StNlink: 2, StSize: 4096, StAtime: t, StMtime: t, StCtime: t}
}

func fileStat(size int64, mode int64, mtime *float64) StatResult {
	if mode < 0o1000 {
		mode |= 0o100000
	}
	t := statTime(mtime)
	return StatResult{StMode: mode, StNlink: 1, StSize: size, StAtime: t, StMtime: t, StCtime: t}
}

func statTime(mtime *float64) float64 {
	if mtime != nil {
		return *mtime
	}
	return float64(time.Now().UnixMicro()) / 1e6
}

// NamedTuple converts the result to the StatResult named tuple the sandbox expects.
func (s StatResult) NamedTuple() monty.NamedTuple {
	return monty.NamedTuple{
		TypeName:   "StatResult",
		FieldNames: append([]string(nil), statFields...),
		Values:     []any{s.StMode, s.StIno, s.StDev, s.StNlink, s.StUID, s.StGID, s.StSize, s.StAtime, s.StMtime, s.StCtime},
	}
}
