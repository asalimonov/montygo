// Command repl is an interactive Monty REPL modelled on the `monty` CLI REPL:
// snippets run in one persistent session, multi-line input follows CPython's
// prompts, and Ctrl-C interrupts a running snippet.
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	monty "github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/examples/internal/montyenv"
)

const (
	statementPrompt    = "❯ "
	continuationPrompt = "… "
	banner             = "Monty v" + monty.Version + " REPL. Type `exit` to exit.\n"
	restartNotice      = "the session was lost; starting a new session\n"
)

var errInterrupted = errors.New("KeyboardInterrupt")

// console is the REPL's terminal. Prompts are written only when interactive.
type console struct {
	in          io.Reader
	out         io.Writer
	errOut      io.Writer
	interactive bool
	interrupts  <-chan struct{}
}

func main() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	interrupts := make(chan struct{}, 1)
	go func() {
		for range signals {
			select {
			case interrupts <- struct{}{}:
			default:
			}
		}
	}()
	info, err := os.Stdin.Stat()
	c := console{
		in:          os.Stdin,
		out:         os.Stdout,
		errOut:      os.Stderr,
		interactive: err == nil && info.Mode()&os.ModeCharDevice != 0,
		interrupts:  interrupts,
	}
	err = run(context.Background(), c, os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", describe(err))
		os.Exit(1)
	}
}

func run(ctx context.Context, c console, args []string) error {
	opts, err := parseOptions(args, c.errOut)
	if err != nil {
		return err
	}
	defer opts.close()
	pool, err := openPool(ctx, opts)
	if err != nil {
		return err
	}
	defer func() { _ = pool.Close(context.Background()) }()
	r := &repl{console: c, pool: pool, opts: opts}
	if err := r.checkout(ctx); err != nil {
		return err
	}
	defer func() { _ = r.session.Close(context.Background()) }()
	if opts.cwd != "" {
		if _, err := r.session.FeedRun(ctx, "", r.feedOptions()); err != nil {
			return err
		}
	}
	if opts.initial != "" {
		if err := r.execute(ctx, opts.initial); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(c.errOut, banner); err != nil {
		return err
	}
	return r.loop(ctx)
}

type options struct {
	scriptName string
	initial    string
	mounts     []*monty.MountDir
	cwd        string
	limits     monty.ResourceLimits
	ws         wsOptions
}

type wsOptions struct {
	url                string
	caFile             string
	insecureSkipVerify bool
}

// openPool dials a remote Monty server when -ws is set, else starts local workers.
func openPool(ctx context.Context, o *options) (*monty.Pool, error) {
	if o.ws.url == "" {
		return monty.New(ctx, montyenv.PoolOptions())
	}
	tlsConfig, err := o.ws.tlsConfig()
	if err != nil {
		return nil, err
	}
	return monty.NewWebSocket(ctx, monty.WebSocketOptions{
		URL: o.ws.url,
		// An interrupt checks out the replacement session before the lost one is released.
		MaxProcesses:   2,
		RequestTimeout: monty.NoRequestTimeout,
		TLSConfig:      tlsConfig,
	})
}

// tlsConfig is nil unless a CA file or skip-verify customises the system defaults.
func (w wsOptions) tlsConfig() (*tls.Config, error) {
	if w.caFile == "" && !w.insecureSkipVerify {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: w.insecureSkipVerify}
	if w.caFile != "" {
		pem, err := os.ReadFile(w.caFile)
		if err != nil {
			return nil, err
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in %s", w.caFile)
		}
		cfg.RootCAs = roots
	}
	return cfg, nil
}

func (o *options) close() {
	for _, m := range o.mounts {
		_ = m.Close()
	}
}

type mountSpecs []string

func (m *mountSpecs) String() string { return strings.Join(*m, ", ") }

func (m *mountSpecs) Set(spec string) error {
	*m = append(*m, spec)
	return nil
}

func parseOptions(args []string, errOut io.Writer) (*options, error) {
	flags := flag.NewFlagSet("repl", flag.ContinueOnError)
	flags.SetOutput(errOut)
	command := flags.String("c", "", "run a Python program passed as a string before the first prompt")
	var mounts mountSpecs
	mountUsage := "mount a host directory: host_path::virtual_path[::mode[::write_limit_bytes]], mode ro (default), rw or overlay"
	flags.Var(&mounts, "m", mountUsage)
	flags.Var(&mounts, "mount", mountUsage)
	cwd := flags.String("cwd", "", "sandbox working directory, an absolute virtual path (default: the first mount, or /)")
	maxDuration := flags.Float64("max-duration", 0, "maximum execution time in seconds (e.g. 0.5)")
	maxMemory := flags.String("max-memory", "", "maximum heap memory (e.g. 1024, 512KB, 10MB, 1GB)")
	gcInterval := flags.Uint64("gc-interval", 0, "run garbage collection every N allocations")
	recursion := flags.Uint64("max-recursion-depth", 0, "maximum call-stack depth (default 1000)")
	suspensions := flags.Uint64("max-suspensions", 0, "maximum suspensions in one session (default 1000)")
	wsURL := flags.String("ws", "", "run sessions on a remote Monty server at this ws:// or wss:// URL")
	wsCA := flags.String("ws-ca", "", "PEM file of CA certificates trusted for a wss:// server")
	wsInsecure := flags.Bool("ws-insecure-skip-verify", false, "skip TLS certificate verification for a wss:// server")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}

	o := &options{
		scriptName: "repl.py",
		initial:    *command,
		cwd:        *cwd,
		ws:         wsOptions{url: *wsURL, caFile: *wsCA, insecureSkipVerify: *wsInsecure},
	}
	switch {
	case *wsURL == "" && (*wsCA != "" || *wsInsecure):
		return nil, errors.New("-ws-ca and -ws-insecure-skip-verify require -ws")
	case flags.NArg() > 1:
		return nil, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args()[1:], " "))
	case flags.NArg() == 1 && *command != "":
		return nil, errors.New("cannot specify both -c and a file")
	case flags.NArg() == 1:
		code, err := os.ReadFile(flags.Arg(0))
		if err != nil {
			return nil, err
		}
		o.scriptName, o.initial = flags.Arg(0), string(code)
	case *command != "":
		o.scriptName = "<string>"
	}

	seconds := *maxDuration
	if math.IsNaN(seconds) || seconds < 0 || seconds > float64(math.MaxInt64)/float64(time.Second) {
		return nil, fmt.Errorf("invalid max duration '%v': expected a non-negative number of seconds", seconds)
	}
	o.limits = monty.ResourceLimits{
		MaxDuration:       time.Duration(seconds * float64(time.Second)),
		GCInterval:        *gcInterval,
		MaxRecursionDepth: *recursion,
		MaxSuspensions:    *suspensions,
	}
	if *maxMemory != "" {
		size, err := parseMemorySize(*maxMemory)
		if err != nil {
			return nil, err
		}
		o.limits.MaxMemory = size
	}
	for _, spec := range mounts {
		dir, err := openMount(spec)
		if err != nil {
			o.close()
			return nil, err
		}
		o.mounts = append(o.mounts, dir)
	}
	return o, nil
}

// parseMemorySize accepts a byte count with an optional KB, MB or GB suffix.
func parseMemorySize(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	number, multiplier := s, uint64(1)
	for _, unit := range []struct {
		suffix     string
		multiplier uint64
	}{{"GB", 1 << 30}, {"gb", 1 << 30}, {"MB", 1 << 20}, {"mb", 1 << 20}, {"KB", 1 << 10}, {"kb", 1 << 10}} {
		if n, ok := strings.CutSuffix(s, unit.suffix); ok {
			number, multiplier = strings.TrimSpace(n), unit.multiplier
			break
		}
	}
	value, err := strconv.ParseUint(number, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid memory size '%s'", s)
	}
	if value > math.MaxUint64/multiplier {
		return 0, fmt.Errorf("memory size '%s' overflows", s)
	}
	return value * multiplier, nil
}

// openMount parses host_path::virtual_path[::mode[::write_limit_bytes]].
func openMount(spec string) (*monty.MountDir, error) {
	parts := strings.Split(spec, "::")
	if len(parts) < 2 || len(parts) > 4 {
		return nil, fmt.Errorf("invalid mount spec '%s': expected host_path::virtual_path[::mode[::write_limit_bytes]]", spec)
	}
	if parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("invalid mount spec '%s': host and virtual paths must not be empty", spec)
	}
	modeName := "ro"
	if len(parts) >= 3 {
		modeName = parts[2]
	}
	mode, ok := map[string]monty.MountMode{
		"ro":      monty.MountReadOnly,
		"rw":      monty.MountReadWrite,
		"overlay": monty.MountOverlay,
	}[modeName]
	if !ok {
		return nil, fmt.Errorf("invalid mount mode '%s' in '%s': expected 'ro', 'rw', or 'overlay'", modeName, spec)
	}
	dirOpts := monty.MountDirOptions{HostPath: parts[0], VirtualPath: parts[1], Mode: mode}
	if len(parts) == 4 {
		if parts[3] == "" {
			return nil, fmt.Errorf("invalid write limit in '%s': value must not be empty", spec)
		}
		limit, err := strconv.ParseUint(parts[3], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid write limit '%s' in '%s': expected a non-negative integer", parts[3], spec)
		}
		dirOpts.WriteBytesLimit = &limit
	}
	dir, err := monty.NewMountDir(dirOpts)
	if err != nil {
		return nil, fmt.Errorf("mount %s: %w", spec, err)
	}
	return dir, nil
}

type repl struct {
	console
	pool    *monty.Pool
	opts    *options
	session *monty.Session
}

func (r *repl) checkout(ctx context.Context) error {
	session, err := r.pool.Checkout(ctx, monty.CheckoutOptions{ScriptName: r.opts.scriptName, Limits: &r.opts.limits})
	if err != nil {
		return err
	}
	r.session = session
	return nil
}

func (r *repl) feedOptions() *monty.FeedOptions {
	return &monty.FeedOptions{
		Mount: r.opts.mounts,
		Cwd:   r.opts.cwd,
		Print: monty.PrintFunc(func(stream monty.Stream, text string) error {
			w := r.out
			if stream == monty.Stderr {
				w = r.errOut
			}
			_, err := io.WriteString(w, text)
			return err
		}),
	}
}

// loop reads lines until EOF or `exit`, collecting multi-line snippets the way
// upstream run_repl does. Upstream detects completeness by parsing; here a
// feed stands in for the parse, and continuationMode reads its syntax error.
func (r *repl) loop(ctx context.Context) error {
	lines := make(chan string)
	readErr := make(chan error, 1)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		reader := bufio.NewReader(r.in)
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				select {
				case lines <- line:
				case <-stop:
					return
				}
			}
			if err != nil {
				readErr <- err
				return
			}
		}
	}()

	pending, mode := "", complete
	for {
		if r.interactive {
			prompt := statementPrompt
			if mode != complete {
				prompt = continuationPrompt
			}
			if _, err := io.WriteString(r.out, prompt); err != nil {
				return err
			}
		}
		var line string
		select {
		case line = <-lines:
		case err := <-readErr:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case <-r.interrupts:
			pending, mode = "", complete
			if r.interactive {
				if _, err := fmt.Fprintln(r.out); err != nil {
					return err
				}
			}
			continue
		case <-ctx.Done():
			return ctx.Err()
		}

		snippet := strings.TrimRightFunc(line, unicode.IsSpace)
		if mode == complete && (snippet == "" || snippet == "exit") {
			if snippet == "exit" {
				return nil
			}
			continue
		}
		pending += snippet + "\n"

		if mode == incompleteBlock {
			if snippet != "" {
				continue
			}
			if err := r.execute(ctx, pending); err != nil {
				return err
			}
			pending, mode = "", complete
			continue
		}
		value, err := r.feed(ctx, pending)
		if next := continuationMode(pending, err); next != complete {
			mode = next
			continue
		}
		if err := r.report(ctx, value, err); err != nil {
			return err
		}
		pending, mode = "", complete
	}
}

func (r *repl) execute(ctx context.Context, code string) error {
	value, err := r.feed(ctx, code)
	return r.report(ctx, value, err)
}

// feed runs code, cancelling it when an interrupt arrives.
func (r *repl) feed(ctx context.Context, code string) (any, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var interrupted atomic.Bool
	done := make(chan struct{})
	go func() {
		select {
		case <-r.interrupts:
			interrupted.Store(true)
			cancel()
		case <-done:
		}
	}()
	value, err := r.session.FeedRun(runCtx, code, r.feedOptions())
	close(done)
	if err != nil && interrupted.Load() {
		return nil, errInterrupted
	}
	return value, err
}

// report prints a result or an error, and replaces a session whose worker is gone.
func (r *repl) report(ctx context.Context, value any, err error) error {
	switch {
	case err == nil:
		if value != nil {
			_, werr := fmt.Fprintln(r.out, display(value))
			return werr
		}
		return nil
	case errors.Is(err, errInterrupted):
		if _, werr := fmt.Fprintln(r.errOut, err); werr != nil {
			return werr
		}
	default:
		if _, werr := fmt.Fprintf(r.errOut, "error: %s\n", describe(err)); werr != nil {
			return werr
		}
		if !sessionLost(err) {
			return nil
		}
	}
	_ = r.session.Close(context.Background())
	if _, err := io.WriteString(r.errOut, restartNotice); err != nil {
		return err
	}
	return r.checkout(ctx)
}

func sessionLost(err error) bool {
	var (
		crashed    *monty.CrashedError
		disconnect *monty.DisconnectError
		shutdown   *monty.ShutdownError
		protocol   *monty.ProtocolError
	)
	return errors.As(err, &crashed) || errors.As(err, &disconnect) || errors.As(err, &shutdown) || errors.As(err, &protocol)
}

// display renders a result like upstream MontyObject's Display: strings raw,
// types as <class '...'>, everything else as repr.
func display(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case monty.Type:
		return "<class '" + v.Name + "'>"
	}
	return monty.Repr(value)
}

func describe(err error) string {
	var montyErr monty.Error
	if errors.As(err, &montyErr) {
		return montyErr.Display(monty.DisplayTraceback)
	}
	return err.Error()
}
