package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/scott/castbot/internal/castlet"
	"github.com/scott/castbot/internal/tooliso"
)

func main() {
	log.SetFlags(0)
	if len(os.Args) > 1 && os.Args[1] == tooliso.SupervisorArg {
		tooliso.RunSupervisor()
		return
	}
	_ = tooliso.SetNonDumpable()
	syscall.Umask(0o077)
	cfg := castlet.Config{
		Addr:       env("CASTLET_ADDR", ":8080"),
		ControlURL: os.Getenv("CASTBOT_URL"),
		AgentID:    os.Getenv("CASTBOT_AGENT_ID"),
		Bootstrap:  os.Getenv("CASTBOT_BOOTSTRAP_TOKEN"),
		CAFile:     os.Getenv("CASTBOT_CA_FILE"),
		AgentDir:   env("CASTLET_AGENT_DIR", "/app/agent"),
		RuntimeURL: env("CASTLET_RUNTIME_URL", "http://127.0.0.1:8081"),
		RuntimeCmd: strings.Fields(env("CASTLET_RUNTIME", "/usr/local/bin/cast-standin --addr 127.0.0.1:8081 --agent /app/agent")),
	}
	if cfg.ControlURL == "" || cfg.AgentID == "" || cfg.Bootstrap == "" {
		log.Fatal("castlet needs CASTBOT_URL, CASTBOT_AGENT_ID, and CASTBOT_BOOTSTRAP_TOKEN")
	}
	if err := castlet.RequireHTTPS(cfg.ControlURL); err != nil {
		log.Fatal("castlet needs an https control plane URL")
	}
	var host *tooliso.RuntimeHost
	if os.Geteuid() == 0 {
		if err := tooliso.PrepareApp(cfg.AgentDir); err != nil {
			log.Fatal("castlet did not start")
		}
		if len(cfg.RuntimeCmd) > 0 {
			started, err := tooliso.StartRuntimeHost(cfg.RuntimeCmd)
			if err != nil {
				log.Fatal("castlet did not start")
			}
			host = started
			defer host.Close()
		}
		if err := tooliso.DropToRuntime(); err != nil {
			log.Fatal("castlet did not start")
		}
	}
	sup, err := castlet.New(cfg)
	if err != nil {
		log.Fatal("castlet did not start")
	}
	sup.UseRuntimeHost(host)
	egressAddr, err := castlet.LoopbackAddr(env("CASTLET_EGRESS_ADDR", "127.0.0.1:8090"))
	if err != nil {
		log.Fatal("castlet egress must listen on 127.0.0.1")
	}
	egressLn, err := net.Listen("tcp", egressAddr)
	if err != nil {
		log.Fatal("castlet egress did not start")
	}
	egressSrv := &http.Server{Handler: http.HandlerFunc(sup.ServeEgress), ReadHeaderTimeout: 10 * time.Second}
	srv := &http.Server{Addr: cfg.Addr, Handler: sup, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	go func() { _ = egressSrv.Serve(egressLn) }()
	go func() {
		if err := sup.Boot(ctx); err != nil {
			log.Printf("boot failed: %s", publicErr(err))
		} else {
			log.Printf("castlet ready")
		}
	}()
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Print("castlet stopped")
			os.Exit(1)
		}
	case <-ctx.Done():
		sup.Close()
		shut, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = egressSrv.Shutdown(shut)
		_ = srv.Shutdown(shut)
	}
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func publicErr(err error) string {
	msg := err.Error()
	if strings.Contains(msg, "bootstrap") || strings.Contains(msg, "token") || strings.Contains(msg, "secret") {
		return "control plane rejected the boot"
	}
	if len(msg) > 120 {
		msg = msg[:120]
	}
	return msg
}
