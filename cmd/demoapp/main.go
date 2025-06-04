package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"

	"github.com/alecthomas/kingpin/v2"
	"github.com/oklog/run"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	versioncollector "github.com/prometheus/client_golang/prometheus/collectors/version"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/promslog/flag"
	"github.com/prometheus/common/version"
	webflag "github.com/prometheus/exporter-toolkit/web/kingpinflag"

	"github.com/ilolicon/demoapp/config"
	"github.com/ilolicon/demoapp/web"
)

func init() {
	prometheus.MustRegister(versioncollector.NewCollector("demoapp"))
	prometheus.Unregister(collectors.NewGoCollector())
}

func main() {
	if os.Getenv("DEBUG") != "" {
		runtime.SetBlockProfileRate(20)
		runtime.SetMutexProfileFraction(20)
	}

	var (
		configFile      = kingpin.Flag("config.file", "Demoapp configuration file name.").Default("config.yaml").String()
		webConfig       = webflag.AddFlags(kingpin.CommandLine, ":80")
		readTimeout     = kingpin.Flag("web.read-timeout", "Maximum duration before timing out read of the request, and closing idle connections.").Default("5m").Duration()
		maxConnections  = kingpin.Flag("web.max-connections", "Maximum number of concurrent connections.").Default("512").Int()
		enableLifecycle = kingpin.Flag("web.enable-lifecycle", "Enable shutdown and relaod via HTTP request.").Default("true").Bool()
	)

	promslogConfig := &promslog.Config{}
	flag.AddFlags(kingpin.CommandLine, promslogConfig)
	kingpin.Version(version.Print("demoapp"))
	kingpin.CommandLine.UsageWriter(os.Stdout) // 帮助文档输出到标准输出(default: 标准错误输出)
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()

	logger := promslog.New(promslogConfig)
	logger.Info("Starting demoapp", "version", version.Info())
	logger.Info("Build context", "build_context", version.BuildContext())

	flagsMap := map[string]string{}
	// Exclude kingpin default flags to expose only Prometheus ones.
	boilerplateFlags := kingpin.New("", "").Version("")
	for _, f := range kingpin.CommandLine.Model().Flags {
		if boilerplateFlags.GetFlag(f.Name) != nil {
			continue
		}
		// filter hidden flags (they are just reserved for compatibility purpose)
		if f.Hidden {
			continue
		}
		flagsMap[f.Name] = f.Value.String()
	}

	webHandler := web.New(logger.With("component", "web"), &web.Options{
		Version: &web.DemoappVersion{
			Version:   version.Version,
			Revision:  version.Revision,
			Branch:    version.Branch,
			BuildUser: version.BuildUser,
			BuildDate: version.BuildDate,
			GoVersion: runtime.Version(),
		},
		Flags: flagsMap,

		ListenAddresses: *webConfig.WebListenAddresses,
		ReadTimeout:     *readTimeout,
		MaxConnections:  *maxConnections,
		EnableLifecycle: *enableLifecycle,
		AppName:         "demoapp",

		Gatherer:   prometheus.DefaultGatherer,
		Registerer: prometheus.DefaultRegisterer,
	})

	// sync.Once is used to make sure we can close the channel at different execution stages(SIGTERM or when the config is loaded).
	type closeOnce struct {
		C     chan struct{}
		once  sync.Once
		Close func()
	}
	// Wait until the server is ready to handle reloading.
	reloadReady := &closeOnce{
		C: make(chan struct{}),
	}
	reloadReady.Close = func() {
		reloadReady.once.Do(func() {
			close(reloadReady.C)
		})
	}
	listeners, err := webHandler.Listeners()
	if err != nil {
		logger.Error("Unable to start web listener", "err", err)
		os.Exit(1)
	}

	configLogger := logger.With("component", "configuration")
	configCoordinator := config.NewCoordinator(*configFile, prometheus.DefaultRegisterer, configLogger)
	configCoordinator.Subscribe(func(conf *config.Config) error {
		return webHandler.ApplyConfig(conf)
	})

	ctxWeb, cancelWeb := context.WithCancel(context.Background())
	defer cancelWeb()

	var g run.Group
	{
		// Termination handler.
		term := make(chan os.Signal, 1)
		signal.Notify(term, os.Interrupt, syscall.SIGTERM)
		cancel := make(chan struct{})
		g.Add(
			func() error {
				// Don't forget to release the reloadReady channel so that waiting blocks can exit normally.
				select {
				case sig := <-term:
					logger.Warn("Received an OS signal, exiting gracefully...", "signal", sig.String())
					reloadReady.Close()
				case <-webHandler.Quit():
					logger.Warn("Received termination request via web service, exiting gracefully...")
				case <-cancel:
					reloadReady.Close()
				}
				return nil
			},
			func(_ error) {
				close(cancel)
				webHandler.SetReady(web.Stopping)
			},
		)
	}
	{
		// Reload handler.
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		cancel := make(chan struct{})
		g.Add(
			func() error {
				<-reloadReady.C

				for {
					select {
					case <-hup:
						// ignore error, already logged in `reload()`
						_ = configCoordinator.Reload()
					case rc := <-webHandler.Reload():
						if err := configCoordinator.Reload(); err != nil {
							rc <- err
						} else {
							rc <- nil
						}
					case <-cancel:
						return nil
					}
				}
			},
			func(_ error) {
				cancel <- struct{}{}
			},
		)
	}
	{
		// Web handler.
		g.Add(
			func() error {
				if err := webHandler.Run(ctxWeb, listeners, *webConfig.WebConfigFile); err != nil {
					return fmt.Errorf("error starting web server: %w", err)
				}
				return nil
			},
			func(_ error) {
				cancelWeb()
			},
		)
	}
	{
		// Initial configuration loading.
		cancel := make(chan struct{})
		g.Add(
			func() error {
				select {
				case <-cancel:
					reloadReady.Close()
					return nil
				default:
					if err := configCoordinator.Reload(); err != nil {
						return fmt.Errorf("error reloading configuration: %w", err)
					}
					reloadReady.Close()
					webHandler.SetReady(web.Ready)
					<-cancel
					return nil
				}
			},
			func(_ error) {
				close(cancel)
			},
		)
	}
	func() { // This function exists so the top of the stack is named 'main.main.funcxxx' and not 'oklog'.
		if err := g.Run(); err != nil {
			logger.Error("Fatal error", "err", err)
			os.Exit(1)
		}
	}()
}
