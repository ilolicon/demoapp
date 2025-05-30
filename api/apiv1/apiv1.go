package apiv1

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ilolicon/demoapp/config"
)

type API struct {
	logger   *slog.Logger
	config   func() *config.Config
	flagsMap map[string]string
}

func NewAPI(logger *slog.Logger, config func() *config.Config, flagsMap map[string]string) *API {
	return &API{
		logger:   logger,
		config:   config,
		flagsMap: flagsMap,
	}
}

func (api *API) Routes() chi.Router {
	wrap := func(f apiFunc) http.HandlerFunc {
		hf := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			result := f(r)
			switch {
			case result.err != nil:
				api.respondError(w, result.err, result.data)
			case result.data != nil:
				api.respond(w, result.data)
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		})
		return hf
	}

	router := chi.NewRouter()
	router.Get("/status/date", wrap(api.serverDate))
	router.Get("/status/flags", wrap(api.serveFlags))
	return router
}

type status string

const (
	statusSuccess status = "success"
	statusError   status = "error"
)

type errorType string

const (
	errorTimeout  errorType = "timeout"
	errorCanceled errorType = "canceled"
	errorExec     errorType = "execution"
	errorBadData  errorType = "bad_data"
	errorInternal errorType = "internal"
	errorNotFound errorType = "not_found"
)

type apiError struct {
	errorType errorType
	err       error
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s: %s", e.errorType, e.err)
}

type response struct {
	Status    status      `json:"status"`
	Data      interface{} `json:"data,omitempty"`
	ErrorType errorType   `json:"errorType,omitempty"`
	Error     string      `json:"error,omitempty"`
}

type apiFuncResult struct {
	data interface{}
	err  *apiError
}

type apiFunc func(r *http.Request) apiFuncResult

func (api *API) serverDate(r *http.Request) apiFuncResult {
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
	return apiFuncResult{data, nil}
}

func (api *API) serveFlags(r *http.Request) apiFuncResult {
	return apiFuncResult{api.flagsMap, nil}
}

func (api *API) respond(w http.ResponseWriter, data interface{}) {
	statusMessage := statusSuccess
	b, err := json.Marshal(&response{
		Status: statusMessage,
		Data:   data,
	})
	if err != nil {
		api.logger.Error("error marshaling json response", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if n, err := w.Write(b); err != nil {
		api.logger.Error("error writing response", "bytesWritten", n, "err", err)
	}
}

func (api *API) respondError(w http.ResponseWriter, apiErr *apiError, data interface{}) {
	b, err := json.Marshal(&response{
		Status:    statusError,
		ErrorType: apiErr.errorType,
		Error:     apiErr.err.Error(),
		Data:      data,
	})
	if err != nil {
		api.logger.Error("error marshaling json response", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var code int
	switch apiErr.errorType {
	case errorBadData:
		code = http.StatusBadRequest
	case errorExec:
		code = 422
	case errorCanceled, errorTimeout:
		code = http.StatusServiceUnavailable
	case errorInternal:
		code = http.StatusInternalServerError
	case errorNotFound:
		code = http.StatusNotFound
	default:
		code = http.StatusInternalServerError
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if n, err := w.Write(b); err != nil {
		api.logger.Error("error writing response", "bytesWritten", n, "err", err)
	}
}
