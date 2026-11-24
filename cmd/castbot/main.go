package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/scott/castbot/internal/assistant"
	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/config"
	"github.com/scott/castbot/internal/demo"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/run"
	"github.com/scott/castbot/internal/runtime"
	"github.com/scott/castbot/internal/store"
	"github.com/scott/castbot/internal/update"
	"github.com/scott/castbot/internal/web"
	"golang.org/x/term"
)

func main() {
	log.SetFlags(0)
	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		serve(args)
	case "create-user":
		createUser(args)
	case "seed-demo":
		seedDemo(args)
	case "create-assistant":
		createAssistant(args)
	case "list-models":
		listModels(args)
	case "save-provider-key":
		saveProviderKey(args)
	case "admin":
		os.Exit(adminCmd(args))
	case "version":
		versionCmd(args)
	case "release":
		releaseCmd(args)
	case "update":
		updateCmd(args)
	default:
		fmt.Fprintln(os.Stderr, "usage: castbot [serve|create-user|seed-demo|create-assistant|list-models|save-provider-key|admin|version|release|update]")
		os.Exit(2)
	}
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	_ = fs.Parse(args)
	cfg, st := openStore()
	defer st.Close()
	if err := cfg.RejectDevEcho(); err != nil {
		log.Fatal(err)
	}
	if err := st.FailAbandoned(context.Background(), run.Interrupted); err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client, stopRuntime := openRuntime(cfg)
	defer stopRuntime()
	app := web.New(st, cfg.SessionKey, client)
	defer app.Close()
	if err := app.RewriteLegacySecretFP(context.Background()); err != nil {
		log.Fatal(err)
	}
	app.SetDevEcho(cfg.DevEcho)
	app.SetUpdate(version, commit, cfg.UpdateMode, update.DefaultCachePath)
	app.SetUpdateApply(func(context.Context) error {
		return startUpdateHelper()
	})
	app.SetDataDir(filepath.Dir(cfg.DBPath))
	if cfg.RuntimeURL != "" {
		app.SetExplicitRuntime(true)
	}
	app.RunReconciler(ctx, time.Minute)
	app.RunStatusRefresh(ctx, 2*time.Second)
	attachMachine(app, cfg)
	recoverCtx, recoverCancel := context.WithTimeout(context.Background(), 30*time.Second)
	app.RecoverReplacements(recoverCtx)
	recoverCancel()
	go app.RetryReplaceDeletes(ctx)
	sweepCtx, sweepCancel := context.WithTimeout(context.Background(), 30*time.Second)
	app.SweepMachines(sweepCtx)
	sweepCancel()
	go func() {
		tick := time.NewTicker(time.Hour)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				_, _ = st.SweepExpiredDrafts(cctx)
				cancel()
			}
		}
	}()
	// Record a newer signed release. Automatic mode starts the helper when
	// a release is ready and no agent is working. Update now uses the same
	// helper. off and a dev build do not fetch.
	go update.Run(ctx, update.RunConfig{
		Version: version,
		Commit:  commit,
		Schema:  store.SchemaVersion,
		Image:   cfg.Image,
		EnvMode: cfg.UpdateMode,
		StoredMode: func(c context.Context) (string, error) {
			return st.InstanceSetting(c, update.ModeKey)
		},
		Apply: func(context.Context) error { return startUpdateHelper() },
		Inflight: func(c context.Context) (int, error) {
			return st.InflightRuns(c)
		},
	})
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()
	logVersion()
	log.Printf("castbot listening on %s", cfg.Addr)
	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Print(err)
			os.Exit(1)
		}
	case <-ctx.Done():
		app.Close()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shut); err != nil {
			log.Print(err)
		}
	}
}

func attachMachine(app *web.Server, cfg config.Config) {
	if cfg.Trusted != "" {
		var nets []*net.IPNet
		var err error
		if cfg.AllowWideProxies {
			nets, err = web.ParseTrustedProxiesAllowWide(cfg.Trusted)
		} else {
			nets, err = web.ParseTrustedProxies(cfg.Trusted)
		}
		if err != nil {
			log.Fatal("trusted proxies")
		}
		app.SetTrustedProxies(nets)
		log.Printf("trusted proxies: %d", len(nets))
	}
	// The host stays off until a token, an image, and an https public URL
	// are all set. A partial set must not exit: the UI still has to serve.
	if cfg.MachineToken == "" || cfg.Image == "" || cfg.PublicURL == "" {
		if cfg.MachineToken != "" || cfg.Image != "" || cfg.PublicURL != "" {
			log.Print("machine host stays off")
		}
		return
	}
	u, err := url.Parse(cfg.PublicURL)
	if err != nil || u.Host == "" || u.Scheme != "https" {
		log.Fatal("machine host needs an https control plane URL")
	}
	m, err := host.New(host.Options{Token: cfg.MachineToken})
	if err != nil {
		log.Fatal("machine host did not start")
	}
	app.SetHost(m, cfg.Image, cfg.PublicURL)
	log.Printf("machine host enabled")
}

func openRuntime(cfg config.Config) (runtime.Client, func()) {
	if cfg.RuntimeURL != "" {
		log.Printf("agent runtime at %s", cfg.RuntimeURL)
		return runtime.NewHTTP(cfg.RuntimeURL), func() {}
	}
	if !cfg.DevEcho {
		return nil, func() {}
	}
	echo := runtime.NewEcho(runtime.EchoOptions{Name: "castbot", WordDelay: 30 * time.Millisecond})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Handler: echo, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	log.Printf("echo runtime on %s", ln.Addr().String())
	return runtime.NewHTTP("http://" + ln.Addr().String()), func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		echo.Close()
	}
}

func createUser(args []string) {
	fs := flag.NewFlagSet("create-user", flag.ExitOnError)
	email := fs.String("email", "", "email address")
	name := fs.String("name", "", "display name")
	workspace := fs.String("workspace", "Personal", "workspace name")
	_ = fs.Parse(args)
	if *email == "" || *name == "" {
		log.Fatal("create-user needs --email and --name")
	}
	password, err := readPassword()
	if err != nil {
		log.Fatal(err)
	}
	if len(password) < 12 || len(password) > 1024 {
		log.Fatal("password must be 12 to 1024 characters")
	}
	cfg, st := openStore()
	defer st.Close()
	n, err := st.CountUsers(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	if n > 0 {
		log.Fatal("an account already exists")
	}
	hash, err := auth.Hash(password, st.Argon())
	if err != nil {
		log.Fatal(err)
	}
	user, err := st.CreateUser(context.Background(), store.NewUser{
		Email: *email, DisplayName: *name, Workspace: *workspace,
		PasswordHash: hash, Theme: "dark",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("created %s\n", user.Email)
	fmt.Println("Sign in with this email and the password. Two-factor can be turned on later in Settings.")
	_ = cfg
}

func seedDemo(args []string) {
	fs := flag.NewFlagSet("seed-demo", flag.ExitOnError)
	email := fs.String("email", "", "account email")
	_ = fs.Parse(args)
	if *email == "" {
		log.Fatal("seed-demo needs --email")
	}
	_, st := openStore()
	defer st.Close()
	if err := demo.Seed(context.Background(), st, *email); err != nil {
		log.Fatal(err)
	}
	fmt.Println("demo data ready")
}

func createAssistant(args []string) {
	fs := flag.NewFlagSet("create-assistant", flag.ExitOnError)
	email := fs.String("email", "", "account email")
	model := fs.String("model", "", "model id")
	_ = fs.Parse(args)
	if *email == "" {
		log.Fatal("create-assistant needs --email")
	}
	_, st := openStore()
	defer st.Close()
	user, err := st.FindUserByEmail(context.Background(), *email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			log.Fatal("no account for that email")
		}
		log.Fatal("could not read the account")
	}
	res, err := assistant.EnsureWithModel(context.Background(), st, user.ID, *model)
	if err != nil {
		if errors.Is(err, assistant.ErrInUse) {
			log.Fatal("an agent already exists")
		}
		if errors.Is(err, store.ErrInvalid) {
			log.Fatal("model is not valid")
		}
		log.Fatalf("could not create CastBot: %v", err)
	}
	if res.Created {
		fmt.Printf("created CastBot %s\n", res.Agent.ID)
	} else {
		fmt.Printf("CastBot already exists %s\n", res.Agent.ID)
	}
	fmt.Printf("/agents/%s\n", res.Agent.ID)
}

func openStore() (config.Config, *store.Store) {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	dir := filepath.Dir(cfg.DBPath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			log.Fatal(err)
		}
	}
	st, err := store.Open(cfg.DBPath, store.Options{SecretsKey: cfg.SecretsKey})
	if err != nil {
		log.Fatal(err)
	}
	return cfg, st
}

func readPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, "Password: ")
		buf, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return string(buf), nil
	}
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("password is empty")
	}
	return strings.TrimRight(sc.Text(), "\r"), nil
}
