package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// maxJSONBody bounds the response body GetJSON decodes.
const maxJSONBody = 1 << 20

// HTTPStatusError reports a response status other than 200 to GetJSON.
type HTTPStatusError struct{ Code int }

func (e *HTTPStatusError) Error() string { return fmt.Sprintf("HTTP status %d", e.Code) }

// GetJSON performs GET <path> on the dialer's server over its transport, with
// the upgrade headers, and decodes a 200 JSON body into out.
func (d *WebSocketDialer) GetJSON(ctx context.Context, path string, headers [][2]string, out any) error {
	server, tlsConfig := d.target(ctx)
	target, err := httpURL(server, path)
	if err != nil {
		return fmt.Errorf("%s: %w", server, err)
	}
	header, host, err := d.upgradeHeader(server, headers)
	if err != nil {
		return err
	}
	header.Set("Accept", "application/json")
	hctx, timeout, cancel := d.bound(ctx)
	defer cancel()
	transport := d.transport(nil, tlsConfig)
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(hctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("%s: %w", target, err)
	}
	req.Header = header
	if host != "" {
		req.Host = host
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	switch {
	case err != nil && ctx.Err() != nil:
		return ctx.Err()
	case err != nil && hctx.Err() != nil:
		return fmt.Errorf("%s: request timed out after %s", target, timeout)
	case err != nil:
		return fmt.Errorf("%s: %w", target, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body := io.LimitReader(resp.Body, maxJSONBody)
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, body)
		return fmt.Errorf("%s: %w", target, &HTTPStatusError{Code: resp.StatusCode})
	}
	if err := json.NewDecoder(body).Decode(out); err != nil {
		return fmt.Errorf("%s: decoding response: %w", target, err)
	}
	return nil
}

// httpURL converts a ws(s) or http(s) URL to the http(s) URL of path under its base path.
func httpURL(raw, path string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return "", err
	}
	switch u.Scheme {
	case "ws", "http":
		u.Scheme = "http"
	case "wss", "https":
		u.Scheme = "https"
	default:
		return "", fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + strings.TrimPrefix(path, "/")
	u.RawPath = ""
	u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
	return u.String(), nil
}
