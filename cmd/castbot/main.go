package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/scott/castbot/internal/auth"
	"github.com/scott/castbot/internal/config"
	"github.com/scott/castbot/internal/demo"
	"github.com/scott/castbot/internal/store"
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
	default:
		fmt.Fprintln(os.Stderr, "usage: castbot [serve|create-user|seed-demo]")
		os.Exit(2)
	}
}

func serve(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	_ = fs.Parse(args)
	cfg, st := openStore()
	defer st.Close()
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           web.New(st, cfg.SessionKey).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("castbot listening on %s", cfg.Addr)
	log.Fatal(srv.ListenAndServe())
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
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		log.Fatal(err)
	}
	user, err := st.CreateUser(context.Background(), store.NewUser{
		Email: *email, DisplayName: *name, Workspace: *workspace,
		PasswordHash: hash, TOTPSecret: secret, Theme: "dark",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("created %s\n", user.Email)
	fmt.Printf("Authenticator secret: %s\n", secret)
	fmt.Printf("%s\n", auth.OTPAuthURL(user.Email, secret))
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
	st, err := store.Open(cfg.DBPath, store.Options{})
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
