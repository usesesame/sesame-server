package server

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type metricKey struct {
	method string
	route  string
	status int
}

type metrics struct {
	mu      sync.Mutex
	counts  map[metricKey]uint64
	seconds map[metricKey]float64
	started time.Time
}

func newMetrics() *metrics {
	return &metrics{counts: map[metricKey]uint64{}, seconds: map[metricKey]float64{}, started: time.Now()}
}

func (m *metrics) observe(method, route string, status int, elapsed time.Duration) {
	key := metricKey{method: method, route: route, status: status}
	m.mu.Lock()
	m.counts[key]++
	m.seconds[key] += elapsed.Seconds()
	m.mu.Unlock()
}

func escapeLabel(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(value)
}

func (m *metrics) render(version string) string {
	m.mu.Lock()
	keys := make([]metricKey, 0, len(m.counts))
	for key := range m.counts {
		keys = append(keys, key)
	}
	counts := make(map[metricKey]uint64, len(m.counts))
	seconds := make(map[metricKey]float64, len(m.seconds))
	for key, value := range m.counts {
		counts[key] = value
		seconds[key] = m.seconds[key]
	}
	started := m.started
	m.mu.Unlock()
	sort.Slice(keys, func(a, b int) bool {
		if keys[a].route != keys[b].route {
			return keys[a].route < keys[b].route
		}
		if keys[a].method != keys[b].method {
			return keys[a].method < keys[b].method
		}
		return keys[a].status < keys[b].status
	})
	var out strings.Builder
	out.WriteString("# HELP sesame_build_info Build information.\n# TYPE sesame_build_info gauge\n")
	fmt.Fprintf(&out, "sesame_build_info{version=\"%s\"} 1\n", escapeLabel(version))
	out.WriteString("# HELP sesame_process_start_time_seconds Start time of the server as a Unix timestamp.\n# TYPE sesame_process_start_time_seconds gauge\n")
	fmt.Fprintf(&out, "sesame_process_start_time_seconds %d\n", started.Unix())
	out.WriteString("# HELP sesame_http_requests_total Requests handled, by route pattern, method and status.\n# TYPE sesame_http_requests_total counter\n")
	for _, key := range keys {
		fmt.Fprintf(&out, "sesame_http_requests_total{route=\"%s\",method=\"%s\",status=\"%s\"} %d\n", escapeLabel(key.route), key.method, strconv.Itoa(key.status), counts[key])
	}
	out.WriteString("# HELP sesame_http_request_duration_seconds_sum Time spent handling requests, by route pattern, method and status.\n# TYPE sesame_http_request_duration_seconds_sum counter\n")
	for _, key := range keys {
		fmt.Fprintf(&out, "sesame_http_request_duration_seconds_sum{route=\"%s\",method=\"%s\",status=\"%s\"} %.6f\n", escapeLabel(key.route), key.method, strconv.Itoa(key.status), seconds[key])
	}
	return out.String()
}

func (s *server) metricsEndpoint(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Forwarded") != "" || r.Header.Get("X-Real-IP") != "" || !privatePeer(peerIP(r)) {
		writeError(w, http.StatusForbidden, "metrics_forbidden", "Metrics are available only to direct requests from this machine or its private network.")
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(s.metrics.render(s.cfg.Version)))
}
