package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mwitkow/go-conntrack"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/route"
	"github.com/prometheus/common/version"
	toolkit_web "github.com/prometheus/exporter-toolkit/web"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/ilolicon/demoapp/config"
	"github.com/ilolicon/demoapp/util/netconnlimit"
	api_v1 "github.com/ilolicon/demoapp/web/api/v1"
)

type ReadyStatus uint32

const (
	NotReady ReadyStatus = iota
	Ready
	Stopping
)

// withStackTracer logs the stack trace in case the request panics. The function
// will re-raise the error which will then be handled by the net/http package.
// It is needed because the go-kit log package doesn't manage properly the
// panics from net/http (see https://github.com/go-kit/kit/issues/233).
func withStackTracer(h http.Handler, l *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				const size = 64 << 10
				buf := make([]byte, size)
				buf = buf[:runtime.Stack(buf, false)]
				l.Error("panic while serving request", "client", r.RemoteAddr, "url", r.URL, "err", err, "stack", buf)
				panic(err)
			}
		}()
		h.ServeHTTP(w, r)
	})
}

type metrics struct {
	requestCounter  *prometheus.CounterVec
	requestDuration *prometheus.HistogramVec
	responseSize    *prometheus.HistogramVec
	readyStatus     prometheus.Gauge
}

func newMetrics(r prometheus.Registerer) *metrics {
	m := &metrics{
		requestCounter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "demoapp_http_requests_total",
				Help: "Counter of HTTP requests.",
			},
			[]string{"handler", "code"},
		),
		requestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:                            "demoapp_http_request_duration_seconds",
				Help:                            "Histogram of latencies for HTTP requests.",
				Buckets:                         []float64{.1, .2, .4, 1, 3, 8, 20, 60, 120},
				NativeHistogramBucketFactor:     1.1,
				NativeHistogramMaxBucketNumber:  100,
				NativeHistogramMinResetDuration: 1 * time.Hour,
			},
			[]string{"handler"},
		),
		responseSize: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "demoapp_http_response_size_bytes",
				Help:    "Histogram of response size for HTTP requests.",
				Buckets: prometheus.ExponentialBuckets(100, 10, 8),
			},
			[]string{"handler"},
		),
		readyStatus: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "demoapp_ready",
			Help: "Whether demoapp startup was fully completed and the server is ready for normal operation.",
		}),
	}

	if r != nil {
		r.MustRegister(m.requestCounter, m.requestDuration, m.responseSize, m.readyStatus)
	}
	return m
}

func (m *metrics) instrumentHandlerWithPrefix(prefix string) func(handlerName string, handler http.HandlerFunc) http.HandlerFunc {
	return func(handlerName string, handler http.HandlerFunc) http.HandlerFunc {
		return m.instrumentHandler(prefix+handlerName, handler)
	}
}

func (m *metrics) instrumentHandler(handlerName string, handler http.HandlerFunc) http.HandlerFunc {
	handlerLabel := prometheus.Labels{"handler": handlerName}
	m.requestCounter.WithLabelValues(handlerName, "200")
	return promhttp.InstrumentHandlerCounter(
		m.requestCounter.MustCurryWith(handlerLabel),
		promhttp.InstrumentHandlerDuration(
			m.requestDuration.MustCurryWith(handlerLabel),
			promhttp.InstrumentHandlerResponseSize(
				m.responseSize.MustCurryWith(handlerLabel),
				handler,
			),
		),
	)
}

type DemoappVersion = api_v1.DemoappVersion

// Options for the web Handler.
type Options struct {
	Version *DemoappVersion
	Flags   map[string]string

	ListenAddresses []string
	ReadTimeout     time.Duration
	MaxConnections  int
	EnableLifecycle bool
	AppName         string

	Gatherer   prometheus.Gatherer
	Registerer prometheus.Registerer
}

// Handler serves various HTTP endpoints for the demoapp server.
type Handler struct {
	mtx    sync.RWMutex
	logger *slog.Logger

	gatherer prometheus.Gatherer
	metrics  *metrics

	apiv1 *api_v1.API

	router      *route.Router
	quitCh      chan struct{}
	quitOnce    sync.Once
	reloadCh    chan chan error
	options     *Options
	config      *config.Config
	versionInfo *DemoappVersion
	flagsMap    map[string]string

	ready atomic.Uint32 // ready is uint32 rather than boolean to be able to use atomic functions.
}

func New(logger *slog.Logger, o *Options) *Handler {
	if logger == nil {
		logger = promslog.NewNopLogger()
	}

	m := newMetrics(o.Registerer)
	router := route.New().
		WithInstrumentation(m.instrumentHandler)

	h := &Handler{
		logger: logger,

		gatherer: o.Gatherer,
		metrics:  m,

		router:      router,
		quitCh:      make(chan struct{}),
		reloadCh:    make(chan chan error),
		options:     o,
		versionInfo: o.Version,
		flagsMap:    o.Flags,
	}
	h.SetReady(NotReady)

	h.apiv1 = api_v1.NewAPI(
		logger,
		func() config.Config {
			h.mtx.RLock()
			defer h.mtx.RUnlock()
			return *h.config
		},
		o.Flags,
		h.testReady,
		h.runtimeInfo,
		h.versionInfo,
		o.Gatherer,
	)

	readyf := h.testReady

	router.Get("/", func(w http.ResponseWriter, r *http.Request) {
		hostname, _ := os.Hostname()
		fmt.Fprintf(w, "demoapp | %s | %s\n", hostname, version.Info())
	})

	router.Get("/version", h.version)
	router.Get("/metrics", promhttp.Handler().ServeHTTP)

	if o.EnableLifecycle {
		router.Post("/-/quit", h.quit)
		router.Put("/-/quit", h.quit)
		router.Post("/-/reload", h.reload)
		router.Put("/-/reload", h.reload)
	} else {
		forbiddenAPINotEnabled := func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte("Lifecycle API is not enabled."))
		}
		router.Post("/-/quit", forbiddenAPINotEnabled)
		router.Put("/-/quit", forbiddenAPINotEnabled)
		router.Post("/-/reload", forbiddenAPINotEnabled)
		router.Put("/-/reload", forbiddenAPINotEnabled)
	}
	router.Get("/-/quit", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte("Only POST or PUT requests allowed"))
	})
	router.Get("/-/reload", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte("Only POST or PUT requests allowed"))
	})

	router.Get("/-/healthy", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "%s is Healthy.\n", o.AppName)
	})
	router.Head("/-/healthy", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router.Get("/-/ready", readyf(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "%s is Ready.\n", o.AppName)
	}))
	router.Head("/-/ready", readyf(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	return h
}

// ApplyConfig updates the config field of the Handler struct.
func (h *Handler) ApplyConfig(conf *config.Config) error {
	h.mtx.Lock()
	defer h.mtx.Unlock()

	h.config = conf
	return nil
}

// Listeners creates the TCP listeners for web requests.
func (h *Handler) Listeners() ([]net.Listener, error) {
	var listeners []net.Listener
	sem := netconnlimit.NewSharedSemaphore(h.options.MaxConnections)
	for _, address := range h.options.ListenAddresses {
		listener, err := h.Listener(address, sem)
		if err != nil {
			return listeners, err
		}
		listeners = append(listeners, listener)
	}
	return listeners, nil
}

// Listener creates the TCP listener for web requests.
func (h *Handler) Listener(address string, sem chan struct{}) (net.Listener, error) {
	h.logger.Info("Start listening for connections", "address", address)

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return listener, err
	}
	listener = netconnlimit.SharedLimitListener(listener, sem)

	// Monitor incoming connections with conntrack.
	listener = conntrack.NewListener(listener,
		conntrack.TrackWithName("http"),
		conntrack.TrackWithTracing())

	return listener, nil
}

// Run serves the HTTP endpoints.
func (h *Handler) Run(ctx context.Context, listeners []net.Listener, webConfig string) error {
	if len(listeners) == 0 {
		var err error
		listeners, err = h.Listeners()
		if err != nil {
			return err
		}
	}

	mux := http.NewServeMux()
	mux.Handle("/", h.router)

	apiPath := "/api"
	av1 := route.New().
		WithInstrumentation(h.metrics.instrumentHandlerWithPrefix("/api/v1")).
		WithInstrumentation(setPathWithPrefix(apiPath + "/v1"))
	h.apiv1.Register(av1)

	mux.Handle(apiPath+"/v1/", http.StripPrefix(apiPath+"/v1", av1))

	errlog := slog.NewLogLogger(h.logger.Handler(), slog.LevelError)

	spanNameFormatter := otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
		return fmt.Sprintf("%s %s", r.Method, r.URL.Path)
	})

	httpSrv := &http.Server{
		Handler:     withStackTracer(otelhttp.NewHandler(mux, "", spanNameFormatter), h.logger),
		ErrorLog:    errlog,
		ReadTimeout: h.options.ReadTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- toolkit_web.ServeMultiple(listeners, httpSrv, &toolkit_web.FlagConfig{WebConfigFile: &webConfig}, h.logger)
	}()

	select {
	case e := <-errCh:
		return e
	case <-ctx.Done():
		httpSrv.Shutdown(ctx)
		return nil
	}
}

func (h *Handler) runtimeInfo() (api_v1.RuntimeInfo, error) {
	status := api_v1.RuntimeInfo{
		GoroutineCount: runtime.NumGoroutine(),
		GoMAXPROCS:     runtime.GOMAXPROCS(0),
	}
	hostname, err := os.Hostname()
	if err != nil {
		return status, fmt.Errorf("Error getting hostname: %w", err)
	}
	status.Hostname = hostname

	return status, nil
}

// SetReady sets the ready status of our web Handler.
func (h *Handler) SetReady(v ReadyStatus) {
	if v == Ready {
		h.ready.Store(uint32(Ready))
		h.metrics.readyStatus.Set(1)
		return
	}

	h.ready.Store(uint32(v))
	h.metrics.readyStatus.Set(0)
}

// Verifies whether the server is ready or not.
// func (h *Handler) isReady() bool {
// 	return ReadyStatus(h.ready.Load()) == Ready
// }

// Checks if server is ready, calls f if it is, returns 503 if it is not.
func (h *Handler) testReady(f http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch ReadyStatus(h.ready.Load()) {
		case Ready:
			f(w, r)
		case NotReady:
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Header().Set("X-Demoapp-Stopping", "false")
			fmt.Fprintf(w, "Service Unavailable")
		case Stopping:
			w.Header().Set("X-Demoapp-Stopping", "true")
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "Service Unavailable")
		default:
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, "Unknown state")
		}
	}
}

// Quit returns the receive-only quit channel.
func (h *Handler) Quit() <-chan struct{} {
	return h.quitCh
}

// Reload returns the receive-only channel that signals configuration reload requests.
func (h *Handler) Reload() <-chan chan error {
	return h.reloadCh
}

func (h *Handler) version(w http.ResponseWriter, _ *http.Request) {
	dec := json.NewEncoder(w)
	if err := dec.Encode(h.versionInfo); err != nil {
		http.Error(w, fmt.Sprintf("error encoding JSON: %s", err), http.StatusInternalServerError)
	}
}

func (h *Handler) quit(w http.ResponseWriter, _ *http.Request) {
	var closed bool
	h.quitOnce.Do(func() {
		closed = true
		close(h.quitCh)
		fmt.Fprintf(w, "Requesting termination... Goodbye!")
	})
	if !closed {
		fmt.Fprintf(w, "Termination already in progress.")
	}
}

func (h *Handler) reload(w http.ResponseWriter, _ *http.Request) {
	rc := make(chan error)
	h.reloadCh <- rc
	if err := <-rc; err != nil {
		http.Error(w, fmt.Sprintf("failed to reload config: %s", err), http.StatusInternalServerError)
	}
}

type pathParam struct{}

// ContextWithPath returns a new context with the given path to be used later
// when logging the query.
func ContextWithPath(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, pathParam{}, path)
}

func setPathWithPrefix(prefix string) func(handlerName string, handler http.HandlerFunc) http.HandlerFunc {
	return func(_ string, handler http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			handler(w, r.WithContext(ContextWithPath(r.Context(), prefix+r.URL.Path)))
		}
	}
}
