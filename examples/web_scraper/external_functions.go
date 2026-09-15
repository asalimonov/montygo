package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/andybalholm/cascadia"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"golang.org/x/net/html"

	monty "github.com/asalimonov/montygo"
	"github.com/asalimonov/montygo/examples/internal/pyargs"
)

// Page is a snapshot of a browser tab; its methods act on the live tab.
type Page struct {
	URL   string `monty:"url"`
	Title string `monty:"title"`
	HTML  string `monty:"html"`
	ID    int64  `monty:"id"`
	ctx   context.Context
	idle  *networkIdle
}

var pageMethods = []string{"go_to", "click", "fill", "select_option", "check", "press", "wait_for_selector", "screenshot", "evaluate", "get_text", "get_attribute"}

func (p *Page) instance() (*monty.ClassInstance, error) {
	return monty.NewClassInstance(p, monty.ClassInstanceOptions{
		EagerAttrs:     monty.Names("url", "title", "html", "id"),
		AllowedMethods: monty.Names(pageMethods...),
	})
}

func (p *Page) asDict() *monty.Dict {
	return monty.NewDict(
		monty.Pair{Key: "url", Value: p.URL},
		monty.Pair{Key: "title", Value: p.Title},
		monty.Pair{Key: "html", Value: p.HTML},
		monty.Pair{Key: "id", Value: p.ID},
	)
}

// CallMethod implements monty.MethodProvider; every method is a coroutine.
func (p *Page) CallMethod(_ context.Context, name string, args []any, kwargs monty.Kwargs) (any, error) {
	switch name {
	case "go_to":
		bound, err := pyargs.Bind("go_to", args, kwargs, pyargs.Required("url"), pyargs.Optional("wait_until", "networkidle"))
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
		return p.async(defaultTimeout, func(ctx context.Context) (any, error) {
			return nil, p.navigate(ctx, url, waitUntil)
		}), nil
	case "click":
		bound, err := pyargs.Bind("click", args, kwargs, pyargs.Required("selector"), pyargs.Optional("force", false))
		if err != nil {
			return nil, err
		}
		selector, err := pyargs.String("selector", bound[0])
		if err != nil {
			return nil, err
		}
		force, err := pyargs.Bool("force", bound[1])
		if err != nil {
			return nil, err
		}
		opts := []chromedp.QueryOption{chromedp.ByQuery}
		if force {
			opts = append(opts, chromedp.NodeReady)
		}
		return p.action(chromedp.Click(selector, opts...)), nil
	case "fill":
		selector, value, err := twoStrings("fill", args, kwargs, "selector", "value")
		if err != nil {
			return nil, err
		}
		var insert chromedp.Action = input.InsertText(value)
		if value == "" {
			insert = chromedp.Evaluate(`document.execCommand('delete')`, nil)
		}
		return p.action(
			chromedp.WaitVisible(selector, chromedp.ByQuery),
			chromedp.Evaluate(jsCall(focusSelectJS, selector), nil),
			insert,
		), nil
	case "select_option":
		selector, value, err := twoStrings("select_option", args, kwargs, "selector", "value")
		if err != nil {
			return nil, err
		}
		return p.action(
			chromedp.WaitVisible(selector, chromedp.ByQuery),
			chromedp.Evaluate(jsCall(selectOptionJS, selector, value), nil),
		), nil
	case "check":
		selector, err := oneString("check", args, kwargs, "selector")
		if err != nil {
			return nil, err
		}
		return p.async(defaultTimeout, func(ctx context.Context) (any, error) {
			var checked bool
			if err := chromedp.Run(ctx, chromedp.WaitVisible(selector, chromedp.ByQuery), chromedp.JavascriptAttribute(selector, "checked", &checked, chromedp.ByQuery)); err != nil {
				return nil, err
			}
			if !checked {
				if err := chromedp.Run(ctx, chromedp.Click(selector, chromedp.ByQuery)); err != nil {
					return nil, err
				}
			}
			return nil, p.idle.wait(ctx)
		}), nil
	case "press":
		selector, key, err := twoStrings("press", args, kwargs, "selector", "key")
		if err != nil {
			return nil, err
		}
		return p.action(chromedp.Focus(selector, chromedp.ByQuery), chromedp.KeyEvent(keyString(key))), nil
	case "wait_for_selector":
		bound, err := pyargs.Bind("wait_for_selector", args, kwargs, pyargs.Required("selector"), pyargs.Optional("timeout", 30000.0))
		if err != nil {
			return nil, err
		}
		selector, err := pyargs.String("selector", bound[0])
		if err != nil {
			return nil, err
		}
		timeout, err := pyargs.Float("timeout", bound[1])
		if err != nil {
			return nil, err
		}
		return p.async(time.Duration(timeout*float64(time.Millisecond)), func(ctx context.Context) (any, error) {
			if err := chromedp.Run(ctx, chromedp.WaitVisible(selector, chromedp.ByQuery)); err != nil {
				return nil, err
			}
			return nil, p.idle.wait(ctx)
		}), nil
	case "screenshot":
		bound, err := pyargs.Bind("screenshot", args, kwargs, pyargs.Optional("full_page", false))
		if err != nil {
			return nil, err
		}
		fullPage, err := pyargs.Bool("full_page", bound[0])
		if err != nil {
			return nil, err
		}
		return p.async(defaultTimeout, func(ctx context.Context) (any, error) {
			var buf []byte
			capture := chromedp.CaptureScreenshot(&buf)
			if fullPage {
				capture = chromedp.FullScreenshot(&buf, 100)
			}
			if err := chromedp.Run(ctx, capture); err != nil {
				return nil, err
			}
			return buf, nil
		}), nil
	case "evaluate":
		expression, err := oneString("evaluate", args, kwargs, "expression")
		if err != nil {
			return nil, err
		}
		return p.async(defaultTimeout, func(ctx context.Context) (any, error) {
			var raw []byte
			err := chromedp.Run(ctx, chromedp.Evaluate(jsCall(evaluateJS, expression), &raw, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
				return p.WithAwaitPromise(true)
			}))
			if err != nil {
				return nil, err
			}
			v, err := decodeJSON(raw)
			if err != nil {
				return nil, err
			}
			return pyStr(v), nil
		}), nil
	case "get_text":
		selector, err := oneString("get_text", args, kwargs, "selector")
		if err != nil {
			return nil, err
		}
		return p.async(defaultTimeout, func(ctx context.Context) (any, error) {
			var text string
			if err := chromedp.Run(ctx, chromedp.TextContent(selector, &text, chromedp.ByQuery, chromedp.NodeReady)); err != nil {
				return nil, err
			}
			return text, nil
		}), nil
	case "get_attribute":
		selector, attr, err := twoStrings("get_attribute", args, kwargs, "selector", "name")
		if err != nil {
			return nil, err
		}
		return p.async(defaultTimeout, func(ctx context.Context) (any, error) {
			var value string
			var ok bool
			if err := chromedp.Run(ctx, chromedp.AttributeValue(selector, attr, &value, &ok, chromedp.ByQuery, chromedp.NodeReady)); err != nil {
				return nil, err
			}
			if !ok {
				return nil, nil
			}
			return value, nil
		}), nil
	}
	return nil, monty.ErrAttrNotExposed
}

func (p *Page) async(timeout time.Duration, fn func(ctx context.Context) (any, error)) *monty.Future {
	return monty.Async(func() (any, error) {
		ctx, cancel := context.WithTimeout(p.ctx, timeout)
		defer cancel()
		v, err := fn(ctx)
		if err != nil {
			return nil, browserError(err)
		}
		return v, nil
	})
}

func (p *Page) action(actions ...chromedp.Action) *monty.Future {
	return p.async(defaultTimeout, func(ctx context.Context) (any, error) {
		if err := chromedp.Run(ctx, actions...); err != nil {
			return nil, err
		}
		return nil, p.idle.wait(ctx)
	})
}

const focusSelectJS = `(selector) => {
  const el = document.querySelector(selector);
  if (!el) throw new Error('No element matches selector ' + selector);
  el.focus();
  if (typeof el.select === 'function') {
    el.select();
  } else {
    const range = document.createRange();
    range.selectNodeContents(el);
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
  }
  return null;
}`

const selectOptionJS = `(selector, value) => {
  const el = document.querySelector(selector);
  if (!el || el.tagName !== 'SELECT') throw new Error('Element is not a <select> element');
  if (!Array.from(el.options).some(o => o.value === value)) throw new Error('No option with value ' + JSON.stringify(value));
  el.value = value;
  el.dispatchEvent(new Event('input', {bubbles: true}));
  el.dispatchEvent(new Event('change', {bubbles: true}));
  return null;
}`

const evaluateJS = `async (expression) => {
  const value = (0, eval)(expression);
  const result = typeof value === 'function' ? await value() : await value;
  return result === undefined ? null : result;
}`

func jsCall(fn string, args ...string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		b, _ := json.Marshal(a)
		quoted[i] = string(b)
	}
	return "(" + fn + ")(" + strings.Join(quoted, ", ") + ")"
}

var namedKeys = map[string]string{
	"Enter": kb.Enter, "Tab": kb.Tab, "Escape": kb.Escape, "Backspace": kb.Backspace, "Delete": kb.Delete,
	"ArrowUp": kb.ArrowUp, "ArrowDown": kb.ArrowDown, "ArrowLeft": kb.ArrowLeft, "ArrowRight": kb.ArrowRight,
	"Home": kb.Home, "End": kb.End, "PageUp": kb.PageUp, "PageDown": kb.PageDown, "Insert": kb.Insert, "Space": " ",
}

func keyString(key string) string {
	if k, ok := namedKeys[key]; ok {
		return k
	}
	return key
}

func oneString(fn string, args []any, kwargs monty.Kwargs, name string) (string, error) {
	bound, err := pyargs.Bind(fn, args, kwargs, pyargs.Required(name))
	if err != nil {
		return "", err
	}
	return pyargs.String(name, bound[0])
}

func twoStrings(fn string, args []any, kwargs monty.Kwargs, first, second string) (string, string, error) {
	bound, err := pyargs.Bind(fn, args, kwargs, pyargs.Required(first), pyargs.Required(second))
	if err != nil {
		return "", "", err
	}
	a, err := pyargs.String(first, bound[0])
	if err != nil {
		return "", "", err
	}
	b, err := pyargs.String(second, bound[1])
	return a, b, err
}

func decodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return decodeJSONValue(dec)
}

func decodeJSONValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '[' {
			items := []any{}
			for dec.More() {
				v, err := decodeJSONValue(dec)
				if err != nil {
					return nil, err
				}
				items = append(items, v)
			}
			_, err := dec.Token()
			return items, err
		}
		d := monty.NewDict()
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return nil, err
			}
			v, err := decodeJSONValue(dec)
			if err != nil {
				return nil, err
			}
			d.Set(key, v)
		}
		_, err := dec.Token()
		return d, err
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i, nil
		}
		return t.Float64()
	}
	return tok, nil
}

func pyStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return monty.Repr(v)
}

// beautifulSoup parses html and returns the document as a Tag.
func beautifulSoup(_ context.Context, args []any, kwargs monty.Kwargs) (any, error) {
	markup, err := oneString("beautiful_soup", args, kwargs, "html")
	if err != nil {
		return nil, err
	}
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(markup))
	if err != nil {
		return nil, err
	}
	return wrapTag(doc.Get(0))
}

// Tag mirrors a BeautifulSoup Tag over a parsed goquery node.
type Tag struct {
	Name        string      `monty:"name"`
	Attrs       *monty.Dict `monty:"attrs"`
	StringValue *string     `monty:"string"`
	Text        string      `monty:"text"`
	HTML        string      `monty:"html"`
	node        *html.Node
}

var multiValuedAttributes = map[string][]string{
	"*":      {"class", "accesskey", "dropzone"},
	"a":      {"rel", "rev"},
	"link":   {"rel", "rev"},
	"td":     {"headers"},
	"th":     {"headers"},
	"form":   {"accept-charset"},
	"object": {"archive"},
	"area":   {"rel"},
	"icon":   {"sizes"},
	"iframe": {"sandbox"},
	"output": {"for"},
}

func isMultiValued(tagName, attr string) bool {
	return slices.Contains(multiValuedAttributes["*"], attr) || slices.Contains(multiValuedAttributes[tagName], attr)
}

func newTag(n *html.Node) (*Tag, error) {
	t := &Tag{Name: "[document]", Attrs: monty.NewDict(), node: n}
	if n.Type == html.ElementNode {
		t.Name = n.Data
		for _, a := range n.Attr {
			key := a.Key
			if a.Namespace != "" {
				key = a.Namespace + ":" + a.Key
			}
			if isMultiValued(n.Data, a.Key) {
				values := []any{}
				for _, v := range strings.Fields(a.Val) {
					values = append(values, v)
				}
				t.Attrs.Set(key, values)
			} else {
				t.Attrs.Set(key, a.Val)
			}
		}
	}
	t.StringValue = nodeString(n)
	t.Text = getText(n, "", false)
	markup, err := goquery.OuterHtml(t.selection())
	if err != nil {
		return nil, err
	}
	t.HTML = markup
	return t, nil
}

func wrapTag(n *html.Node) (*monty.ClassInstance, error) {
	t, err := newTag(n)
	if err != nil {
		return nil, err
	}
	return t.instance()
}

func wrapTags(nodes []*html.Node) ([]any, error) {
	out := make([]any, 0, len(nodes))
	for _, n := range nodes {
		ci, err := wrapTag(n)
		if err != nil {
			return nil, err
		}
		out = append(out, ci)
	}
	return out, nil
}

func (t *Tag) instance() (*monty.ClassInstance, error) {
	return monty.NewClassInstance(t, monty.ClassInstanceOptions{
		EagerAttrs:     monty.Names("name", "attrs", "string", "text", "html"),
		AllowedMethods: monty.Names("find", "find_all", "select", "select_one", "get", "get_text", "children"),
	})
}

func (t *Tag) selection() *goquery.Selection {
	return goquery.NewDocumentFromNode(t.node).Selection
}

func (t *Tag) asDict() *monty.Dict {
	var s any
	if t.StringValue != nil {
		s = *t.StringValue
	}
	return monty.NewDict(
		monty.Pair{Key: "name", Value: t.Name},
		monty.Pair{Key: "attrs", Value: t.Attrs},
		monty.Pair{Key: "string", Value: s},
		monty.Pair{Key: "text", Value: t.Text},
		monty.Pair{Key: "html", Value: t.HTML},
	)
}

// CallMethod implements monty.MethodProvider.
func (t *Tag) CallMethod(_ context.Context, name string, args []any, kwargs monty.Kwargs) (any, error) {
	switch name {
	case "find", "find_all":
		params := []pyargs.Param{pyargs.Optional("name", nil), pyargs.Optional("attrs", nil), pyargs.Optional("string", nil)}
		if name == "find_all" {
			params = append(params, pyargs.Optional("limit", nil))
		}
		bound, err := pyargs.Bind(name, args, kwargs, params...)
		if err != nil {
			return nil, err
		}
		s, err := newStrainer(bound[0], bound[1], bound[2])
		if err != nil {
			return nil, err
		}
		if name == "find" {
			found := t.findAll(s, 1)
			if len(found) == 0 {
				return nil, nil
			}
			return wrapTag(found[0])
		}
		limit := 0
		if bound[3] != nil {
			l, err := pyargs.Int("limit", bound[3])
			if err != nil {
				return nil, err
			}
			limit = int(l)
		}
		return wrapTags(t.findAll(s, limit))
	case "select", "select_one":
		selector, err := oneString(name, args, kwargs, "selector")
		if err != nil {
			return nil, err
		}
		matcher, err := cascadia.Compile(selector)
		if err != nil {
			return nil, monty.Raise("ValueError", fmt.Sprintf("Malformed CSS selector %q: %v", selector, err))
		}
		nodes := t.selection().FindMatcher(matcher).Nodes
		if name == "select" {
			return wrapTags(nodes)
		}
		if len(nodes) == 0 {
			return nil, nil
		}
		return wrapTag(nodes[0])
	case "get":
		bound, err := pyargs.Bind("get", args, kwargs, pyargs.Required("key"), pyargs.Optional("default", nil))
		if err != nil {
			return nil, err
		}
		key, err := pyargs.String("key", bound[0])
		if err != nil {
			return nil, err
		}
		if v, ok := t.Attrs.Get(key); ok {
			return v, nil
		}
		return bound[1], nil
	case "get_text":
		bound, err := pyargs.Bind("get_text", args, kwargs, pyargs.Optional("separator", ""), pyargs.Optional("strip", false))
		if err != nil {
			return nil, err
		}
		separator, err := pyargs.String("separator", bound[0])
		if err != nil {
			return nil, err
		}
		strip, err := pyargs.Bool("strip", bound[1])
		if err != nil {
			return nil, err
		}
		return getText(t.node, separator, strip), nil
	case "children":
		if _, err := pyargs.Bind("children", args, kwargs); err != nil {
			return nil, err
		}
		out := []any{}
		for c := t.node.FirstChild; c != nil; c = c.NextSibling {
			switch c.Type {
			case html.ElementNode:
				ci, err := wrapTag(c)
				if err != nil {
					return nil, err
				}
				out = append(out, ci)
			case html.TextNode, html.CommentNode, html.DoctypeNode:
				if c.Data != "" {
					out = append(out, c.Data)
				}
			}
		}
		return out, nil
	}
	return nil, monty.ErrAttrNotExposed
}

func (t *Tag) findAll(s *strainer, limit int) []*html.Node {
	var out []*html.Node
	var walk func(n *html.Node) bool
	walk = func(n *html.Node) bool {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if s.match(c) {
				out = append(out, c)
				if limit > 0 && len(out) >= limit {
					return false
				}
			}
			if !walk(c) {
				return false
			}
		}
		return true
	}
	walk(t.node)
	return out
}

type attrFilter struct {
	key   string
	value any
}

type strainer struct {
	names []string
	attrs []attrFilter
	text  *string
}

func newStrainer(name, attrs, text any) (*strainer, error) {
	s := &strainer{}
	switch n := name.(type) {
	case nil:
	case string:
		s.names = []string{n}
	case []any:
		for _, item := range n {
			str, ok := item.(string)
			if !ok {
				return nil, monty.Raise("TypeError", "find_all() name list items must be str")
			}
			s.names = append(s.names, str)
		}
	default:
		return nil, monty.Raise("TypeError", fmt.Sprintf("name must be str, list[str] or None, not %s", monty.Repr(name)))
	}
	switch a := attrs.(type) {
	case nil:
	case string:
		s.attrs = append(s.attrs, attrFilter{key: "class", value: a})
	case *monty.Dict:
		for _, p := range a.Pairs() {
			key, ok := p.Key.(string)
			if !ok {
				return nil, monty.Raise("TypeError", "attrs keys must be str")
			}
			switch p.Value.(type) {
			case nil, bool, string:
			default:
				return nil, monty.Raise("TypeError", fmt.Sprintf("attrs[%s] must be str, bool or None", monty.Repr(key)))
			}
			s.attrs = append(s.attrs, attrFilter{key: key, value: p.Value})
		}
	default:
		return nil, monty.Raise("TypeError", "attrs must be dict[str, str] or None")
	}
	if text != nil {
		str, ok := text.(string)
		if !ok {
			return nil, monty.Raise("TypeError", "string must be str or None")
		}
		s.text = &str
	}
	return s, nil
}

func (s *strainer) match(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	if len(s.names) > 0 && !slices.Contains(s.names, n.Data) {
		return false
	}
	for _, f := range s.attrs {
		val, ok := attribute(n, f.key)
		switch want := f.value.(type) {
		case nil:
			if ok {
				return false
			}
		case bool:
			if ok != want {
				return false
			}
		case string:
			if !ok {
				return false
			}
			if val != want && !(isMultiValued(n.Data, f.key) && slices.Contains(strings.Fields(val), want)) {
				return false
			}
		}
	}
	if s.text != nil {
		str := nodeString(n)
		if str == nil || *str != *s.text {
			return false
		}
	}
	return true
}

func attribute(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func nodeString(n *html.Node) *string {
	c := n.FirstChild
	if c == nil || c.NextSibling != nil {
		return nil
	}
	switch c.Type {
	case html.ElementNode:
		return nodeString(c)
	case html.TextNode, html.CommentNode, html.DoctypeNode:
		s := c.Data
		return &s
	}
	return nil
}

func isStringContainer(name string) bool {
	return name == "script" || name == "style" || name == "template"
}

func stringContainer(n *html.Node) string {
	for p := n; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && isStringContainer(p.Data) {
			return p.Data
		}
	}
	return ""
}

func getText(root *html.Node, separator string, strip bool) string {
	want := ""
	if root.Type == html.ElementNode && isStringContainer(root.Data) {
		want = root.Data
	}
	var parts []string
	var walk func(n *html.Node, container string)
	walk = func(n *html.Node, container string) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			switch c.Type {
			case html.TextNode:
				if container != want {
					continue
				}
				text := c.Data
				if strip {
					text = strings.TrimSpace(text)
					if text == "" {
						continue
					}
				}
				parts = append(parts, text)
			case html.ElementNode:
				next := container
				if isStringContainer(c.Data) {
					next = c.Data
				}
				walk(c, next)
			}
		}
	}
	walk(root, stringContainer(root))
	return strings.Join(parts, separator)
}
