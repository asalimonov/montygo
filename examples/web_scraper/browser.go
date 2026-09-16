package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/asalimonov/montygo/monterr"
	"github.com/asalimonov/montygo/sandbox/host"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/asalimonov/montygo/examples/internal/pyargs"
)

const (
	defaultTimeout  = 30 * time.Second
	networkIdleTime = 500 * time.Millisecond
)

var waitUntilValues = []string{"commit", "domcontentloaded", "load", "networkidle"}

const contentJS = `(() => {
  let html = '';
  if (document.doctype) html = new XMLSerializer().serializeToString(document.doctype);
  if (document.documentElement) html += document.documentElement.outerHTML;
  return html;
})()`

// Browser is a headless Chrome instance shared by every page the sandbox opens.
type Browser struct {
	ctx         context.Context
	allocCancel context.CancelFunc
	mu          sync.Mutex
	nextID      int64
	tabs        []context.CancelFunc
}

func startBrowser(ctx context.Context) (*Browser, error) {
	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, chromedp.DefaultExecAllocatorOptions[:]...)
	bctx, _ := chromedp.NewContext(allocCtx)
	if err := chromedp.Run(bctx); err != nil {
		allocCancel()
		return nil, fmt.Errorf("start headless Chrome: %w", err)
	}
	return &Browser{ctx: bctx, allocCancel: allocCancel}, nil
}

// Close closes every page and shuts the browser down.
func (b *Browser) Close() {
	b.mu.Lock()
	tabs := b.tabs
	b.tabs = nil
	b.mu.Unlock()
	for _, cancel := range tabs {
		cancel()
	}
	_ = chromedp.Cancel(b.ctx)
	b.allocCancel()
}

// openPage opens a URL in a new tab and returns a Page snapshot.
func (b *Browser) openPage(_ context.Context, args []any, kwargs host.Kwargs) (any, error) {
	bound, err := pyargs.Bind("open_page", args, kwargs, pyargs.Required("url"), pyargs.Optional("wait_until", "networkidle"))
	if err != nil {
		return nil, err
	}
	url, err := pyargs.String("url", bound[0])
	if err != nil {
		return nil, err
	}
	waitUntil, err := parseWaitUntil(bound[1])
	if err != nil {
		return nil, err
	}
	return host.Async(func() (any, error) {
		p, err := b.newPage()
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(p.ctx, defaultTimeout)
		defer cancel()
		if err := p.navigate(ctx, url, waitUntil); err != nil {
			return nil, browserError(err)
		}
		if err := chromedp.Run(ctx, chromedp.Location(&p.URL), chromedp.Title(&p.Title), chromedp.Evaluate(contentJS, &p.HTML)); err != nil {
			return nil, browserError(err)
		}
		return p.instance()
	}), nil
}

func (b *Browser) newPage() (*Page, error) {
	tabCtx, cancel := chromedp.NewContext(b.ctx)
	idle := &networkIdle{inflight: map[network.RequestID]struct{}{}}
	chromedp.ListenTarget(tabCtx, idle.observe)
	if err := chromedp.Run(tabCtx, network.Enable()); err != nil {
		cancel()
		return nil, browserError(err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	b.tabs = append(b.tabs, cancel)
	return &Page{ID: b.nextID, ctx: tabCtx, idle: idle}, nil
}

func parseWaitUntil(v any) (string, error) {
	s, err := pyargs.String("wait_until", v)
	if err != nil {
		return "", err
	}
	for _, allowed := range waitUntilValues {
		if s == allowed {
			return s, nil
		}
	}
	return "", monterr.Raise("ValueError", fmt.Sprintf("wait_until: expected one of (load|domcontentloaded|networkidle|commit), got %q", s))
}

func browserError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return monterr.Raise("TimeoutError", "Timeout exceeded: "+err.Error())
	}
	return err
}

func (p *Page) navigate(ctx context.Context, url, waitUntil string) error {
	switch waitUntil {
	case "load":
		return chromedp.Run(ctx, chromedp.Navigate(url))
	case "networkidle":
		if err := chromedp.Run(ctx, chromedp.Navigate(url)); err != nil {
			return err
		}
		return p.idle.wait(ctx)
	}
	lctx, stop := context.WithCancel(ctx)
	defer stop()
	loaded := make(chan struct{}, 1)
	chromedp.ListenTarget(lctx, func(ev any) {
		if _, ok := ev.(*page.EventDomContentEventFired); ok {
			select {
			case loaded <- struct{}{}:
			default:
			}
		}
	})
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		_, _, errorText, _, err := page.Navigate(url).Do(ctx)
		if err != nil {
			return err
		}
		if errorText != "" {
			return fmt.Errorf("page load error %s", errorText)
		}
		return nil
	}))
	if err != nil || waitUntil == "commit" {
		return err
	}
	select {
	case <-loaded:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type networkIdle struct {
	mu       sync.Mutex
	inflight map[network.RequestID]struct{}
	last     time.Time
}

func (w *networkIdle) observe(ev any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch e := ev.(type) {
	case *network.EventRequestWillBeSent:
		w.inflight[e.RequestID] = struct{}{}
	case *network.EventLoadingFinished:
		delete(w.inflight, e.RequestID)
	case *network.EventLoadingFailed:
		delete(w.inflight, e.RequestID)
	default:
		return
	}
	w.last = time.Now()
}

func (w *networkIdle) idle() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.inflight) == 0 && time.Since(w.last) >= networkIdleTime
}

func (w *networkIdle) wait(ctx context.Context) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for !w.idle() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return nil
}
