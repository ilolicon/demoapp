package v1

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/ilolicon/demoapp/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/route"
)

type status string

const (
	statusSuccess status = "success"
	statusError   status = "error"

	// Non-standard status code (originally introduced by nginx) for the case when a client closes
	// the connection while the server is still processing the request.
	statusClientClosedConnection = 499
)

type errorType string

const (
	errorNone          errorType = ""
	errorTimeout       errorType = "timeout"
	errorCanceled      errorType = "canceled"
	errorExec          errorType = "execution"
	errorBadData       errorType = "bad_data"
	errorInternal      errorType = "internal"
	errorUnavailable   errorType = "unavailable"
	errorNotFound      errorType = "not_found"
	errorNotAcceptable errorType = "not_acceptable"
)

type apiError struct {
	typ errorType
	err error
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s: %s", e.typ, e.err)
}

// DemoappVersion contains build information about Demoapp.
type DemoappVersion struct {
	Version   string `json:"version"`
	Revision  string `json:"revision"`
	Branch    string `json:"branch"`
	BuildUser string `json:"buildUser"`
	BuildDate string `json:"buildDate"`
	GoVersion string `json:"goVersion"`
}

// RuntimeInfo contains runtime information about Demoapp.
type RuntimeInfo struct {
	Hostname       string `json:"hostname"`
	GoroutineCount int    `json:"goroutineCount"`
	GoMAXPROCS     int    `json:"GOMAXPROCS"`
}

// Response contains a response to a HTTP API request.
type Response struct {
	Status    status      `json:"status"`
	Data      interface{} `json:"data,omitempty"`
	ErrorType errorType   `json:"errorType,omitempty"`
	Error     string      `json:"error,omitempty"`
}

type apiFuncResult struct {
	data      interface{}
	err       *apiError
	finalizer func()
}

type apiFunc func(r *http.Request) apiFuncResult

type API struct {
	logger   *slog.Logger
	config   func() config.Config
	flagsMap map[string]string
	ready    func(http.HandlerFunc) http.HandlerFunc

	buildInfo   *DemoappVersion
	runtimeInfo func() (RuntimeInfo, error)
	gatherer    prometheus.Gatherer
}

func NewAPI(
	logger *slog.Logger,
	config func() config.Config,
	flagsMap map[string]string,
	ready func(http.HandlerFunc) http.HandlerFunc,
	runtimeInfo func() (RuntimeInfo, error),
	buildInfo *DemoappVersion,
	gatherer prometheus.Gatherer,
) *API {
	return &API{
		logger:      logger,
		config:      config,
		flagsMap:    flagsMap,
		ready:       ready,
		runtimeInfo: runtimeInfo,
		buildInfo:   buildInfo,
		gatherer:    gatherer,
	}
}

// Register the API's endpoints in the given router.:w
func (api *API) Register(r *route.Router) {
	wrap := func(f apiFunc) http.HandlerFunc {
		hf := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			result := f(r)
			if result.finalizer != nil {
				defer result.finalizer()
			}
			if result.err != nil {
				api.respondError(w, result.err, result.data)
				return
			}

			if result.data != nil {
				api.respond(w, r, result.data)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
		return api.ready(hf)
	}

	r.Get("/status/config", wrap(api.serveConfig))
	r.Get("/status/runtimeinfo", wrap(api.serveRuntimeInfo))
	r.Get("/status/buildinfo", wrap(api.serveBuildInfo))
	r.Get("/status/flags", wrap(api.serveFlags))
	r.Get("/status/date", wrap(api.serveDate))

}

func (api *API) respond(w http.ResponseWriter, req *http.Request, data interface{}) {
	statusMessage := statusSuccess

	resp := &Response{
		Status: statusMessage,
		Data:   data,
	}
	b, err := json.Marshal(resp)
	if err != nil {
		api.logger.Error("error marshaling response", "url", req.URL, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if n, err := w.Write(b); err != nil {
		api.logger.Error("error writing response", "url", req.URL, "bytesWritten", n, "err", err)
	}
}

func (api *API) respondError(w http.ResponseWriter, apiErr *apiError, data interface{}) {
	b, err := json.Marshal(&Response{
		Status:    statusError,
		ErrorType: apiErr.typ,
		Error:     apiErr.err.Error(),
		Data:      data,
	})
	if err != nil {
		api.logger.Error("error marshaling json response", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var code int
	switch apiErr.typ {
	case errorBadData:
		code = http.StatusBadRequest
	case errorExec:
		code = http.StatusUnprocessableEntity
	case errorCanceled:
		code = statusClientClosedConnection
	case errorTimeout:
		code = http.StatusServiceUnavailable
	case errorInternal:
		code = http.StatusInternalServerError
	case errorNotFound:
		code = http.StatusNotFound
	case errorNotAcceptable:
		code = http.StatusNotAcceptable
	default:
		code = http.StatusInternalServerError
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if n, err := w.Write(b); err != nil {
		api.logger.Error("error writing response", "bytesWritten", n, "err", err)
	}
}

type demoappConfig struct {
	YAML string `json:"yaml"`
}

func (api *API) serveRuntimeInfo(_ *http.Request) apiFuncResult {
	status, err := api.runtimeInfo()
	if err != nil {
		return apiFuncResult{status, &apiError{errorInternal, err}, nil}
	}
	return apiFuncResult{status, nil, nil}
}

func (api *API) serveBuildInfo(_ *http.Request) apiFuncResult {
	return apiFuncResult{api.buildInfo, nil, nil}
}

func (api *API) serveConfig(_ *http.Request) apiFuncResult {
	cfg := &demoappConfig{
		YAML: api.config().String(),
	}
	return apiFuncResult{cfg, nil, nil}
}

func (api *API) serveFlags(_ *http.Request) apiFuncResult {
	return apiFuncResult{api.flagsMap, nil, nil}
}

func (api *API) serveDate(r *http.Request) apiFuncResult {
	var data string

	cfg := api.config()
	switch cfg.DateFormat {
	case "RFC3339":
		data = time.Now().Format(time.RFC3339)
	case "RFC3339Nano":
		data = time.Now().Format(time.RFC3339Nano)
	case "RFC1123":
		data = time.Now().Format(time.RFC1123)
	case "UnixDate":
		data = time.Now().Format(time.UnixDate)
	case "Unix":
		data = fmt.Sprintf("%d", time.Now().Unix())
	default:
		data = time.Now().Format(time.DateTime)
	}
	return apiFuncResult{data, nil, nil}
}
