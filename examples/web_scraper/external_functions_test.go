package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/asalimonov/montygo"
)

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

const soupFixture = `<!DOCTYPE html><html><head><title>T</title><style>p { color: red }</style></head><body>` +
	`<div id="main" class="a b"><p>Hello <b>World</b></p><p>  second  </p><!--note--><script>var x = 1;</script>` +
	`<a href="/x" rel="nofollow noopener">link</a></div><template><p>hidden</p></template></body></html>`

func mustSoup(t *testing.T, markup string) *Tag {
	t.Helper()
	v, err := beautifulSoup(t.Context(), []any{markup}, nil)
	require.NoError(t, err)
	return asTag(t, v)
}

func asTag(t *testing.T, v any) *Tag {
	t.Helper()
	ci, ok := v.(*montygo.ClassInstance)
	require.True(t, ok, "expected a wrapped Tag, got %T", v)
	tag, ok := ci.Instance().(*Tag)
	require.True(t, ok)
	return tag
}

func call(t *testing.T, tag *Tag, name string, args []any, kwargs montygo.Kwargs) any {
	t.Helper()
	v, err := tag.CallMethod(t.Context(), name, args, kwargs)
	require.NoError(t, err)
	return v
}

func tagNames(t *testing.T, v any) []string {
	t.Helper()
	items, ok := v.([]any)
	require.True(t, ok, "expected a list, got %T", v)
	names := []string{}
	for _, item := range items {
		names = append(names, asTag(t, item).Name)
	}
	return names
}

func TestTagAttributes(t *testing.T) {
	soup := mustSoup(t, soupFixture)
	require.Equal(t, "[document]", soup.Name)
	require.Nil(t, soup.StringValue)
	require.Equal(t, "THello World  second  link", soup.Text)

	div := asTag(t, call(t, soup, "find", []any{"div"}, nil))
	require.Equal(t, "div", div.Name)
	require.True(t, montygo.Equal(montygo.NewDict(montygo.Pair{Key: "id", Value: "main"}, montygo.Pair{Key: "class", Value: []any{"a", "b"}}), div.Attrs))
	require.Equal(t, `<div id="main" class="a b"><p>Hello <b>World</b></p><p>  second  </p><!--note--><script>var x = 1;</script><a href="/x" rel="nofollow noopener">link</a></div>`, div.HTML)

	b := asTag(t, call(t, soup, "find", []any{"b"}, nil))
	require.Equal(t, "World", *b.StringValue)
	p := asTag(t, call(t, soup, "find", []any{"p"}, nil))
	require.Nil(t, p.StringValue)
	require.Equal(t, "Hello World", p.Text)
	style := asTag(t, call(t, soup, "find", []any{"style"}, nil))
	require.Equal(t, "p { color: red }", style.Text)
}

func TestTagFind(t *testing.T) {
	soup := mustSoup(t, soupFixture)
	require.Nil(t, call(t, soup, "find", []any{"table"}, nil))
	require.Equal(t, "div", asTag(t, call(t, soup, "find", nil, montygo.Kwargs{"attrs": montygo.NewDict(montygo.Pair{Key: "class", Value: "b"})})).Name)
	require.Equal(t, "b", asTag(t, call(t, soup, "find", nil, montygo.Kwargs{"string": "World"})).Name)
	require.Equal(t, "a", asTag(t, call(t, soup, "find", []any{"a", montygo.NewDict(montygo.Pair{Key: "rel", Value: "noopener"})}, nil)).Name)
	require.Equal(t, []string{"p", "p", "a", "p"}, tagNames(t, call(t, soup, "find_all", []any{[]any{"p", "a"}}, nil)))
	require.Equal(t, []string{"p"}, tagNames(t, call(t, soup, "find_all", []any{"p"}, montygo.Kwargs{"limit": int64(1)})))
	div := asTag(t, call(t, soup, "find", []any{"div"}, nil))
	require.Equal(t, []string{"p", "b", "p", "script", "a"}, tagNames(t, call(t, div, "find_all", nil, nil)))

	_, err := soup.CallMethod(t.Context(), "find", []any{int64(1)}, nil)
	require.EqualError(t, err, "TypeError: name must be str, list[str] or None, not 1")
	_, err = soup.CallMethod(t.Context(), "find", nil, montygo.Kwargs{"nam": "p"})
	require.EqualError(t, err, "TypeError: find() got an unexpected keyword argument 'nam'")
}

func TestTagSelect(t *testing.T) {
	soup := mustSoup(t, soupFixture)
	require.Equal(t, []string{"p", "p"}, tagNames(t, call(t, soup, "select", []any{"div.a > p"}, nil)))
	a := asTag(t, call(t, soup, "select_one", []any{"a[href]"}, nil))
	require.Equal(t, "link", a.Text)
	require.Nil(t, call(t, soup, "select_one", []any{"table td"}, nil))
	_, err := soup.CallMethod(t.Context(), "select", []any{"div["}, nil)
	require.ErrorContains(t, err, "ValueError: Malformed CSS selector \"div[\"")
}

func TestTagGetAndText(t *testing.T) {
	soup := mustSoup(t, soupFixture)
	a := asTag(t, call(t, soup, "find", []any{"a"}, nil))
	require.Equal(t, "/x", call(t, a, "get", []any{"href"}, nil))
	require.Equal(t, []any{"nofollow", "noopener"}, call(t, a, "get", []any{"rel"}, nil))
	require.Nil(t, call(t, a, "get", []any{"missing"}, nil))
	require.Equal(t, "fallback", call(t, a, "get", []any{"missing"}, montygo.Kwargs{"default": "fallback"}))

	div := asTag(t, call(t, soup, "find", []any{"div"}, nil))
	require.Equal(t, "Hello World  second  link", call(t, div, "get_text", nil, nil))
	require.Equal(t, "Hello|World|second|link", call(t, div, "get_text", []any{"|"}, montygo.Kwargs{"strip": true}))
	script := asTag(t, call(t, div, "find", []any{"script"}, nil))
	require.Equal(t, "var x = 1;", call(t, script, "get_text", nil, nil))
}

func TestTagChildren(t *testing.T) {
	soup := mustSoup(t, soupFixture)
	p := asTag(t, call(t, soup, "find", []any{"p"}, nil))
	children := call(t, p, "children", nil, nil).([]any)
	require.Len(t, children, 2)
	require.Equal(t, "Hello ", children[0])
	require.Equal(t, "b", asTag(t, children[1]).Name)

	doc := call(t, soup, "children", nil, nil).([]any)
	require.Equal(t, "html", doc[0])
	require.Equal(t, "html", asTag(t, doc[1]).Name)
}

func TestPageMethodsInHeadlessChrome(t *testing.T) {
	browser := requireBrowser(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<!DOCTYPE html><html><head><title>Fixture</title></head><body>
<input name="q">
<select id="s"><option value="a">A</option><option value="b">B</option></select>
<input type="checkbox" id="c">
<button id="go" type="button" onclick="document.querySelector('#out').textContent = 'clicked ' + document.querySelector('input[name=q]').value">Go</button>
<input id="k" onkeydown="if (event.key === 'Enter') document.querySelector('#out').dataset.key = 'enter'">
<div id="out"></div>
</body></html>`)
	})
	mux.HandleFunc("/next", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<!DOCTYPE html><html><head><title>Next</title></head><body>
<script>setTimeout(() => { const d = document.createElement('div'); d.id = 'late'; d.textContent = 'late!'; document.body.appendChild(d); }, 200);</script>
</body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	s := &scraper{
		pool: newTestPool(t),
		out:  io.Discard,
		externals: map[string]any{
			"open_page":      montygo.FunctionFunc(browser.openPage),
			"beautiful_soup": montygo.FunctionFunc(beautifulSoup),
		},
	}
	code := `page = await open_page(` + strconv.Quote(srv.URL+"/") + `, wait_until='load')
await page.fill('input[name="q"]', 'monty')
await page.click('#go')
clicked = await page.get_text('#out')
await page.select_option('#s', 'b')
selected = await page.evaluate('document.querySelector("#s").value')
await page.check('#c')
checked = await page.evaluate('() => document.querySelector("#c").checked')
await page.press('#k', 'Enter')
key = await page.get_attribute('#out', 'data-key')
missing = await page.get_attribute('#out', 'data-missing')
structured = await page.evaluate('[1, 2.5, {"a": null}]')
await page.go_to(` + strconv.Quote(srv.URL+"/next") + `, wait_until='domcontentloaded')
await page.wait_for_selector('#late', timeout=5000)
late = await page.get_text('#late')
title = await page.evaluate('document.title')
shot = await page.screenshot(full_page=True)
[page.title, page.id, clicked, selected, checked, key, missing, structured, late, title, shot[:4] == b'\x89PNG']`
	msg, err := s.runCode(t.Context(), code, nil, true)
	require.NoError(t, err)
	require.Equal(t, `["Fixture",1,"clicked monty","b","True","enter",null,"[1, 2.5, {'a': None}]","late!","Next",true]`, msg)

	msg, err = s.runCode(t.Context(), "page = await open_page("+strconv.Quote(srv.URL+"/")+")\nawait page.wait_for_selector('#never', timeout=300)", nil, true)
	require.NoError(t, err)
	require.Contains(t, msg, "Error running code: ")
	require.Contains(t, msg, "TimeoutError: Timeout exceeded")
}
