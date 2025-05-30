package chilog

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

var _ middleware.LogEntry = (*LogEntry)(nil)

type LogEntry struct {
	Logger *slog.Logger // field logger interface, created by RequestLogger
}

func (l *LogEntry) Write(status, bytes int, header http.Header, elapsed time.Duration, extra interface{}) {
	l.Logger.Info("request complete",
		"resp_status", status,
		"resp_bytes_length", bytes,
		"resp_elapsed_ms", float64(elapsed.Nanoseconds())/1000000.0,
	)
}

func (l *LogEntry) Panic(rec interface{}, stack []byte) {
	l.Logger.Error("panic recovered",
		"stack", string(stack),
		"panic", fmt.Sprintf("%+v", rec),
	)
}
