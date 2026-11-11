package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/replace"
	"github.com/scott/castbot/internal/store"
)

func adminCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: castbot admin [machines|replace-machine]")
		return 2
	}
	switch args[0] {
	case "machines":
		return adminMachines(args[1:])
	case "replace-machine":
		return adminReplace(args[1:])
	default:
		fmt.Fprintln(os.Stderr, "usage: castbot admin [machines|replace-machine]")
		return 2
	}
}

func adminMachines(args []string) int {
	fs := flag.NewFlagSet("machines", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, st := openStore()
	defer st.Close()
	return listMachines(context.Background(), st, cfg.Image, os.Stdout)
}

func listMachines(ctx context.Context, st *store.Store, image string, out io.Writer) int {
	rows, err := st.LiveMachines(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "machine list failed")
		return 1
	}
	target := replace.ImageTag(image)
	for _, vm := range rows {
		agent, err := st.GetAgent(ctx, vm.UserID, vm.AgentID)
		if err != nil {
			continue
		}
		tag := replace.ImageTag(vm.Image)
		line := agent.Name + "  " + vm.State + "  " + tag
		if target != "" && tag != target {
			line += "  update available"
		}
		if pending, err := st.CleanupPending(ctx, vm.UserID, vm.AgentID); err == nil && pending {
			line += "  cleanup pending"
		}
		fmt.Fprintln(out, line)
	}
	return 0
}

func adminReplace(args []string) int {
	fs := flag.NewFlagSet("replace-machine", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dry := fs.Bool("dry-run", false, "print the plan without calling the machine API")
	yes := fs.Bool("yes", false, "skip the name prompt")
	noCopy := fs.Bool("no-copy", false, "leave memory and files behind")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: castbot admin replace-machine <agent> [--dry-run] [--yes] [--no-copy]")
		return 2
	}
	cfg, st := openStore()
	defer st.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	agent, err := st.AgentByIDOrSlug(ctx, fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent not found")
		return 1
	}
	if *dry {
		return printPlan(ctx, st, agent, cfg.Image, os.Stdout)
	}
	if !castbotUp(cfg.PublicURL) {
		fmt.Fprintln(os.Stderr, "castbot is not reachable")
		return 1
	}
	plan, err := replace.DryRun(ctx, st, agent.UserID, agent.ID, cfg.Image)
	if err != nil {
		fmt.Fprintln(os.Stderr, "replace plan failed")
		return 1
	}
	writePlan(os.Stdout, plan)
	if plan.Block != "" {
		fmt.Fprintln(os.Stdout, "blocked: "+plan.Block)
		return 1
	}
	if !*yes && !confirmAgentName(os.Stdin, os.Stderr, agent.Name) {
		fmt.Fprintln(os.Stderr, "The name did not match.")
		return 1
	}
	if cfg.MachineToken == "" || cfg.Image == "" || cfg.PublicURL == "" {
		fmt.Fprintln(os.Stderr, "No machine host is configured.")
		return 1
	}
	m, err := host.New(host.Options{Token: cfg.MachineToken})
	if err != nil {
		fmt.Fprintln(os.Stderr, "machine host did not start")
		return 1
	}
	return runReplace(ctx, st, m, replace.Options{
		UserID: agent.UserID, AgentID: agent.ID,
		ActorKind: "cli", ActorID: "cli", Actor: "cli",
		Image: cfg.Image, PublicURL: cfg.PublicURL, NoCopy: *noCopy,
		DataDir: dataDir(cfg.DBPath),
		Progress: func(title string) {
			fmt.Fprintln(os.Stdout, title)
		},
	}, os.Stdout)
}

func printPlan(ctx context.Context, st *store.Store, agent store.Agent, image string, out io.Writer) int {
	plan, err := replace.DryRun(ctx, st, agent.UserID, agent.ID, image)
	if err != nil {
		fmt.Fprintln(os.Stderr, "replace plan failed")
		return 1
	}
	writePlan(out, plan)
	if plan.Block != "" {
		fmt.Fprintln(out, "blocked: "+plan.Block)
		return 1
	}
	return 0
}

func writePlan(out io.Writer, plan replace.Plan) {
	fmt.Fprintf(out, "agent: %s\n", plan.AgentName)
	fmt.Fprintf(out, "machine: %s\n", plan.MachineID)
	fmt.Fprintf(out, "state: %s\n", plan.State)
	fmt.Fprintf(out, "image: %s\n", replace.ImageTag(plan.CurrentImage))
	fmt.Fprintf(out, "target: %s\n", replace.ImageTag(plan.TargetImage))
}

func runReplace(ctx context.Context, st *store.Store, h host.AgentHost, opt replace.Options, out io.Writer) int {
	res, err := replace.Run(ctx, st, h, opt)
	if err == nil {
		fmt.Fprintf(out, "replaced %s on %s\n", res.AgentName, replace.ImageTag(res.NewImage))
		return 0
	}
	var human *replace.NeedsHuman
	if errors.As(err, &human) {
		fmt.Fprintln(out, human.Error())
		return 2
	}
	var kept *replace.Kept
	if errors.As(err, &kept) {
		fmt.Fprintln(out, kept.Error())
		return 1
	}
	var blocked *replace.Blocked
	if errors.As(err, &blocked) {
		fmt.Fprintln(out, blocked.Error())
		return 1
	}
	fmt.Fprintln(os.Stderr, "replace failed")
	return 1
}

func confirmAgentName(in io.Reader, out io.Writer, name string) bool {
	fmt.Fprintf(out, "Type %s to replace this machine: ", name)
	sc := bufio.NewScanner(in)
	return sc.Scan() && strings.TrimSpace(sc.Text()) == name
}

func castbotUp(raw string) bool {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return false
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(raw + "/login")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

func dataDir(dbPath string) string {
	i := strings.LastIndex(dbPath, "/")
	if i <= 0 {
		return ""
	}
	return dbPath[:i]
}
