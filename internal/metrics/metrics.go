package metrics

import (
	"fmt"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	registry *prometheus.Registry

	agentsConnected prometheus.Gauge
	streamsOpened   *prometheus.CounterVec
	streamsActive   *prometheus.GaugeVec
	bytesIn         *prometheus.CounterVec
	bytesOut        *prometheus.CounterVec
	handshakeFails  prometheus.Counter
	httpRequests    *prometheus.CounterVec
}

func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		registry: reg,
		agentsConnected: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mishmesh_agents_connected",
			Help: "Number of currently connected agents.",
		}),
		streamsOpened: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mishmesh_streams_opened_total",
			Help: "Total streams opened.",
		}, []string{"kind"}),
		streamsActive: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "mishmesh_streams_active",
			Help: "Currently active streams.",
		}, []string{"kind"}),
		bytesIn: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mishmesh_bytes_in_total",
			Help: "Total inbound bytes.",
		}, []string{"kind"}),
		bytesOut: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mishmesh_bytes_out_total",
			Help: "Total outbound bytes.",
		}, []string{"kind"}),
		handshakeFails: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mishmesh_handshake_failures_total",
			Help: "Total handshake failures.",
		}),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mishmesh_http_requests_total",
			Help: "Total HTTP requests by status class.",
		}, []string{"code"}),
	}
	reg.MustRegister(
		m.agentsConnected,
		m.streamsOpened,
		m.streamsActive,
		m.bytesIn,
		m.bytesOut,
		m.handshakeFails,
		m.httpRequests,
	)
	return m
}

func (m *Metrics) Handler() http.Handler {
	if m == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	}
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

func (m *Metrics) AgentConnected() {
	if m == nil {
		return
	}
	m.agentsConnected.Inc()
}

func (m *Metrics) AgentDisconnected() {
	if m == nil {
		return
	}
	m.agentsConnected.Dec()
}

func (m *Metrics) StreamOpened(kind string) {
	if m == nil {
		return
	}
	m.streamsOpened.WithLabelValues(kind).Inc()
	m.streamsActive.WithLabelValues(kind).Inc()
}

func (m *Metrics) StreamClosed(kind string) {
	if m == nil {
		return
	}
	m.streamsActive.WithLabelValues(kind).Dec()
}

func (m *Metrics) AddBytes(kind string, in, out int64) {
	if m == nil {
		return
	}
	m.bytesIn.WithLabelValues(kind).Add(float64(in))
	m.bytesOut.WithLabelValues(kind).Add(float64(out))
}

func (m *Metrics) HandshakeFailure() {
	if m == nil {
		return
	}
	m.handshakeFails.Inc()
}

func (m *Metrics) HTTPRequest(code int) {
	if m == nil {
		return
	}
	class := fmt.Sprintf("%dxx", code/100)
	m.httpRequests.WithLabelValues(class).Inc()
}
