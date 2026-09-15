package network

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Metrics maps `name{k="v",...}` (labels sorted) to a value.
type Metrics map[string]float64

func scrapeMetrics(ctx context.Context, base string) (Metrics, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/metrics", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics returned %d", resp.StatusCode)
	}
	out := Metrics{}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cut := strings.LastIndexByte(line, ' ')
		if cut < 0 {
			continue
		}
		v, err := strconv.ParseFloat(line[cut+1:], 64)
		if err != nil {
			continue
		}
		name, labels := parseSeries(line[:cut])
		out[metricKey(name, labels)] = v
	}
	return out, scanner.Err()
}

func parseSeries(series string) (string, map[string]string) {
	open := strings.IndexByte(series, '{')
	if open < 0 {
		return series, nil
	}
	labels := map[string]string{}
	for _, pair := range strings.Split(strings.TrimSuffix(series[open+1:], "}"), ",") {
		k, v, ok := strings.Cut(pair, "=")
		if ok {
			labels[k] = strings.Trim(v, `"`)
		}
	}
	return series[:open], labels
}

// Get returns a series value; absent series of a counter family read as 0.
func (m Metrics) Get(name string, labels map[string]string) float64 {
	return m[metricKey(name, labels)]
}

func metricKey(name string, labels map[string]string) string {
	if len(labels) == 0 {
		return name
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%q", k, labels[k])
	}
	return name + "{" + strings.Join(parts, ",") + "}"
}
