// Command cast-standin is the Cast-compatible runtime baked into the
// shared image for local tests. A later slice can point CASTLET_RUNTIME
// at the real Cast process without changing castlet.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/scott/castbot/internal/runtime"
)

func main() {
	log.SetFlags(0)
	addr := flag.String("addr", "127.0.0.1:8081", "listen address")
	agent := flag.String("agent", "/app/agent", "agent directory")
	flag.Parse()
	name := agentName(*agent)
	var delay time.Duration
	if raw := strings.TrimSpace(os.Getenv("CAST_STANDIN_WORD_DELAY")); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			log.Fatal("stand-in delay")
		}
		delay = parsed
	}
	count := strings.TrimSpace(os.Getenv("CASTLET_SECRET_COUNT"))
	if count == "" {
		count = "0"
	}
	log.Printf("stand-in ready name=%s secrets=%s", name, count)
	echo := runtime.NewEcho(runtime.EchoOptions{Name: name, WordDelay: delay})
	defer echo.Close()
	srv := &http.Server{
		Addr: *addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && (r.URL.Path == "/" || r.URL.Path == "") {
				log.Printf("turn")
			}
			echo.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}

func agentName(dir string) string {
	raw, err := os.ReadFile(dir + "/agent.yaml")
	if err != nil {
		return "agent"
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "name:") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		name = strings.Trim(name, `"'`)
		if name != "" {
			return name
		}
	}
	return "agent"
}
