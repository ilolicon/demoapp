package api

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/common/version"

	"github.com/ilolicon/demoapp/api/apiv1"
	"github.com/ilolicon/demoapp/config"
)

// Options for the web Handler.
type Options struct {
	ListenAddress string
	Flags         map[string]string
}

type Handler struct {
	mtx    sync.RWMutex
	logger *slog.Logger

	apiv1 *apiv1.API

	router   chi.Router
	reloadCh chan chan error
	options  *Options
	config   *config.Config

	ready atomic.Bool // ready is uint32 rather than boolean to be able to use atomic functions.
}

func New(logger *slog.Logger, o *Options) *Handler {
	router := chi.NewRouter()

	h := &Handler{
		logger: logger,

		router:   router,
		reloadCh: make(chan chan error),
		options:  o,
	}

	h.apiv1 = apiv1.NewAPI(
		logger,
		func() *config.Config {
			h.mtx.RLock()
			defer h.mtx.RUnlock()
			return h.config
		},
		o.Flags,
	)

	router.Get("/", func(w http.ResponseWriter, r *http.Request) {
		hostname, _ := os.Hostname()
		fmt.Fprintf(w, "demoapp | %s | %s\n", hostname, version.Info())
	})

	router.Post("/-/reload", h.reload)
	router.Put("/-/reload", h.reload)

	readyf := h.testReady

	router.Get("/-/healthy", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "OK.\n")
	})
	router.Get("/-/ready", readyf(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "OK.\n")
	}))

	router.Mount("/api/v1", h.apiv1.Routes())

	return h
}

// ApplyConfig updates the config field of the Handler struct
func (h *Handler) ApplyConfig(conf *config.Config) error {
	h.mtx.Lock()
	defer h.mtx.Unlock()

	h.config = conf
	return nil
}

// Run serves the HTTP endpoints.
func (h *Handler) Run(ctx context.Context) error {
	h.logger.Info("Start listening for connections", "address", h.options.ListenAddress)
	listener, err := net.Listen("tcp", h.options.ListenAddress)
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Handler: h.router,
	}

	errCh := make(chan error)
	go func() {
		errCh <- httpSrv.Serve(listener)
	}()

	select {
	case e := <-errCh:
		return e
	case <-ctx.Done():
		httpSrv.Shutdown(ctx)
		return nil
	}
}

// Reload returns the receive-only channel that signals configuration reload requests.
func (h *Handler) Reload() <-chan chan error {
	return h.reloadCh
}

func (h *Handler) reload(w http.ResponseWriter, r *http.Request) {
	rc := make(chan error)
	h.reloadCh <- rc
	if err := <-rc; err != nil {
		http.Error(w, fmt.Sprintf("failed to reload config: %s", err), http.StatusInternalServerError)
		return
	}

	io.WriteString(w, "OK")
}

// Ready sets Handler to be ready.
func (h *Handler) Ready() {
	h.ready.Store(true)
}

// Verifies whether the server is ready or not.
func (h *Handler) isReady() bool {
	return h.ready.Load()
}

// Checks if server is ready, calls f if it is, returns 503 if it is not.
func (h *Handler) testReady(f http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.isReady() {
			f(w, r)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "Service Unavailable")
		}
	}
}
