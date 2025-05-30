package chilog

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
)

type Logger struct {
	Logger *slog.Logger
}

func (l *Logger) NewLogEntry(r *http.Request) middleware.LogEntry {
	entry := &LogEntry{Logger: l.Logger}

	fields := make([]interface{}, 0, 16)
	if reqID := middleware.GetReqID(r.Context()); reqID != "" {
		fields = append(fields, "req_id", reqID)
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	fields = append(fields, "http_scheme", scheme)
	fields = append(fields, "http_proto", r.Proto)
	fields = append(fields, "http_method", r.Method)
	fields = append(fields, "remote_addr", r.RemoteAddr)
	fields = append(fields, "user_agent", r.UserAgent())
	fields = append(fields, "uri", fmt.Sprintf("%s://%s%s", scheme, r.Host, r.RequestURI))

	entry.Logger = entry.Logger.With(fields...)
	return entry
}
