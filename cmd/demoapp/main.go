package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/promslog/flag"
	"github.com/prometheus/common/version"
	webflag "github.com/prometheus/exporter-toolkit/web/kingpinflag"

	"github.com/ilolicon/demoapp/api"
	"github.com/ilolicon/demoapp/config"
)

func main() {
	os.Exit(run())
}

func run() int {
	var (
		configFile = kingpin.Flag("config.file", "Path to config file.").Default("./config.yaml").String()
		webConfig  = webflag.AddFlags(kingpin.CommandLine, ":80")
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

	listenAddress := (*webConfig.WebListenAddresses)[0]
	webHandler := api.New(logger.With("component", "web"), &api.Options{
		ListenAddress: listenAddress,
		Flags:         flagsMap,
	})

	configLogger := logger.With("component", "configuration")
	configCoordinator := config.NewCoordinator(
		*configFile,
		prometheus.DefaultRegisterer,
		configLogger)
	configCoordinator.Subscribe(func(conf *config.Config) error {
		return webHandler.ApplyConfig(conf)
	})

	if err := configCoordinator.Reload(); err != nil {
		return 1
	}

	ctxWeb, cancelWeb := context.WithCancel(context.Background())
	defer cancelWeb()

	srvc := make(chan error, 1)
	go func() {
		defer close(srvc)

		if err := webHandler.Run(ctxWeb); err != nil {
			logger.Error("Error starting HTTP server", "err", err)
			srvc <- err
		}
	}()

	var (
		reloadReady = make(chan struct{})
		hup         = make(chan os.Signal, 1)
		term        = make(chan os.Signal, 1)
	)
	signal.Notify(hup, syscall.SIGHUP)
	signal.Notify(term, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-reloadReady
		for {
			select {
			case <-ctxWeb.Done():
				return
			case <-hup:
				// ignore error, already logged in `reload()`
				_ = configCoordinator.Reload()
			case rc := <-webHandler.Reload():
				if err := configCoordinator.Reload(); err != nil {
					rc <- err
				} else {
					rc <- nil
				}
			}
		}
	}()

	// Wait for reload or termination signals.
	close(reloadReady) // Unblock SIGHUP handler.
	webHandler.Ready()

	for {
		select {
		case <-term:
			logger.Info("Received SIGTERM, exiting gracefully...")
			cancelWeb()
		case err := <-srvc:
			if err != nil {
				return 1
			}
			return 0
		}
	}
}
