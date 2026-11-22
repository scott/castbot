package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/scott/castbot/internal/castcfg"
	"github.com/scott/castbot/internal/castcfg/modellist"
	"github.com/scott/castbot/internal/store"
	"golang.org/x/term"
)

func listModels(args []string) {
	fs := flag.NewFlagSet("list-models", flag.ExitOnError)
	provider := fs.String("provider", "", "provider id")
	_ = fs.Parse(args)
	key, err := readLine()
	if err != nil {
		log.Fatal("could not read the model key")
	}
	cat := modellist.New()
	if err := writeModelList(context.Background(), cat, *provider, key, os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func saveProviderKey(args []string) {
	fs := flag.NewFlagSet("save-provider-key", flag.ExitOnError)
	email := fs.String("email", "", "account email")
	provider := fs.String("provider", "", "provider id")
	_ = fs.Parse(args)
	if strings.TrimSpace(*email) == "" || strings.TrimSpace(*provider) == "" {
		log.Fatal("save-provider-key needs --email and --provider")
	}
	key, err := readLine()
	if err != nil {
		log.Fatal("could not read the model key")
	}
	_, st := openStore()
	defer st.Close()
	if err := applyProviderKey(context.Background(), st, *email, *provider, key); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			log.Fatal("no account for that email")
		}
		log.Fatal("could not save the model key")
	}
	fmt.Println("saved model key")
}

// writeModelList prints one live model per line as id, tab, label.
// A manifest fallback is an error, so a rejected key is not shown as a catalog.
// The key is not written.
func writeModelList(ctx context.Context, cat *modellist.Catalog, provider, key string, w io.Writer) error {
	provider = strings.TrimSpace(provider)
	key = strings.TrimSpace(key)
	if !keyedProvider(provider) {
		return errors.New("unknown provider")
	}
	if !oneToken(key) {
		return errors.New("model key is not a single token")
	}
	list, err := cat.List(ctx, provider, key)
	if err != nil || list.Fallback {
		return errors.New("the provider did not return a model list")
	}
	n := 0
	for _, model := range list.Models {
		id := strings.TrimSpace(model.ID)
		label := strings.TrimSpace(model.Label)
		if id == "" || strings.Contains(id, key) || strings.Contains(label, key) {
			continue
		}
		if label == "" {
			label = id
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\n", id, label); err != nil {
			return err
		}
		n++
	}
	if n == 0 {
		return errors.New("the provider did not return a model list")
	}
	return nil
}

// applyProviderKey stores the key on the account named by email.
// The value is encrypted in the vault. It is not logged.
func applyProviderKey(ctx context.Context, st *store.Store, email, provider, key string) error {
	provider = strings.TrimSpace(provider)
	key = strings.TrimSpace(key)
	if !keyedProvider(provider) || !oneToken(key) {
		return store.ErrInvalid
	}
	user, err := st.FindUserByEmail(ctx, email)
	if err != nil {
		return err
	}
	name := modelSecretName(provider)
	label := castcfg.ProviderName(provider)
	if label == "" {
		label = provider
	}
	existing, err := st.SecretByName(ctx, user.ID, name)
	if errors.Is(err, store.ErrNotFound) {
		_, err = st.CreateSecret(ctx, user.ID, store.SecretInput{
			Name: name, Label: label, Provider: provider, Value: key,
		})
		return err
	}
	if err != nil {
		return err
	}
	_, err = st.ReplaceSecret(ctx, user.ID, existing.ID, key)
	return err
}

func keyedProvider(provider string) bool {
	for _, p := range castcfg.KeyedProviders() {
		if p.Key == provider {
			return true
		}
	}
	return false
}

func modelSecretName(provider string) string {
	return "MODEL_" + strings.ToUpper(strings.ReplaceAll(provider, "-", "_"))
}

func oneToken(value string) bool {
	if value == "" || len(value) > 4096 {
		return false
	}
	return !strings.ContainsAny(value, " \t\r\n")
}

func readLine() (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		buf, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(buf)), nil
	}
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return "", err
		}
		return "", errors.New("empty")
	}
	return strings.TrimSpace(sc.Text()), nil
}
