package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/promslog/flag"
	"github.com/prometheus/common/route"
	"github.com/prometheus/common/version"
	"github.com/prometheus/exporter-toolkit/web"
	webflag "github.com/prometheus/exporter-toolkit/web/kingpinflag"
	"gopkg.in/yaml.v3"

	"github.com/ilolicon/demoapp/config"
)

var (
	AppName string
	Version string

	promslogConfig *promslog.Config
	logger         *slog.Logger

	configFile = kingpin.Flag("config.file", "Path to config file.").Default("./config.yaml").String()
	webConfig  = webflag.AddFlags(kingpin.CommandLine, ":80")
)

func Register(r *route.Router, reloadCh chan<- chan error, logger *slog.Logger) {
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		hostname, _ := os.Hostname()
		fmt.Fprintf(w, "%s | %s | %s\n", AppName, hostname, Version)
	})

	r.Get("/config", func(w http.ResponseWriter, r *http.Request) {
		cfg, _ := config.LoadFile(*configFile)
		b, _ := yaml.Marshal(cfg)
		w.Write(b)
	})

	r.Get("/logger", func(w http.ResponseWriter, r *http.Request) {
		logger.Debug("logger", "method", r.Method, "path", r.URL.Path)
		logger.Info("logger", "method", r.Method, "path", r.URL.Path)
		logger.Warn("logger", "method", r.Method, "path", r.URL.Path)
		logger.Error("logger", "method", r.Method, "path", r.URL.Path)

		fmt.Fprint(w, promslogConfig.Level.String())
	})

	r.Post("/-/reload", func(w http.ResponseWriter, req *http.Request) {
		errc := make(chan error)
		defer close(errc)

		reloadCh <- errc
		if err := <-errc; err != nil {
			http.Error(w, fmt.Sprintf("failed to reload config: %s", err), http.StatusInternalServerError)
		}
	})
}

func run() int {
	promslogConfig = &promslog.Config{}
	flag.AddFlags(kingpin.CommandLine, promslogConfig)
	kingpin.Version(version.Print(AppName))
	kingpin.CommandLine.UsageWriter(os.Stdout) // 帮助文档输出到标准输出(default: 标准错误输出)
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()

	logger = promslog.New(promslogConfig)
	logger.Info(fmt.Sprintf("Starting %s", AppName), "version", Version)
	cfg, err := config.LoadFile(*configFile)
	if err != nil {
		logger.Error(fmt.Sprintf("Error loading config (--config.file=%s)", *configFile), "err", err)
		return 1
	}
	if cfg.LogLevel != "" {
		if err := promslogConfig.Level.Set(cfg.LogLevel); err != nil {
			logger.Error(fmt.Sprintf("Error setting log level from config (%s)", cfg.LogLevel), "err", err)
		}
	}

	configLogger := logger.With("component", "configuration")
	configCoordinator := config.NewCoordinator(
		*configFile,
		prometheus.DefaultRegisterer,
		configLogger,
	)
	configCoordinator.Subscribe(func(c *config.Config) error {
		promslogConfig.Level.Set(c.LogLevel)
		return nil
	})

	router := route.New()
	webReload := make(chan chan error)
	Register(router, webReload, logger)

	srv := &http.Server{Handler: router}
	srvc := make(chan struct{})
	go func() {
		if err := web.ListenAndServe(srv, webConfig, logger); !errors.Is(err, http.ErrServerClosed) {
			logger.Error("Listen error", "err", err)
			close(srvc)
		}
		if err := http.ListenAndServe(":80", nil); err != nil {
			logger.Error("Listen error", "err", err)

			close(srvc)
		}
	}()

	var (
		hup  = make(chan os.Signal, 1)
		term = make(chan os.Signal, 1)
	)
	signal.Notify(hup, syscall.SIGHUP)
	signal.Notify(term, os.Interrupt, syscall.SIGTERM)

	for {
		select {
		case <-hup:
			// ignore error, already logged in `reload()`
			_ = configCoordinator.Reload()
		case errc := <-webReload:
			errc <- configCoordinator.Reload()
		case <-term:
			logger.Info("Received SIGTERM, exiting gracefully...")
			return 0
		case <-srvc:
			return 1
		}
	}

}

func main() {
	os.Exit(run())
}
