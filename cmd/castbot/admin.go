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

	"github.com/scott/castbot/internal/backup"
	"github.com/scott/castbot/internal/config"
	"github.com/scott/castbot/internal/host"
	"github.com/scott/castbot/internal/replace"
	"github.com/scott/castbot/internal/store"
)

func adminCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: castbot admin [machines|replace-machine|rollback-runtime|backup|backups|restore]")
		return 2
	}
	switch args[0] {
	case "machines":
		return adminMachines(args[1:])
	case "replace-machine":
		return adminReplace(args[1:])
	case "rollback-runtime":
		return adminRollback(args[1:])
	case "backup":
		return adminBackup(args[1:])
	case "backups":
		return adminBackups(args[1:])
	case "restore":
		return adminRestore(args[1:])
	default:
		fmt.Fprintln(os.Stderr, "usage: castbot admin [machines|replace-machine|rollback-runtime|backup|backups|restore]")
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
	ctx, stop := interruptContext()
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
	m, err := newMachineHost(host.Options{Token: cfg.MachineToken})
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

var newMachineHost = func(opt host.Options) (host.AgentHost, error) {
	return host.New(opt)
}

func interruptContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func confirmAgentName(in io.Reader, out io.Writer, name string) bool {
	return confirmTyped(in, out, "Type "+name+" to replace this machine: ", name)
}

func confirmTyped(in io.Reader, out io.Writer, prompt, want string) bool {
	fmt.Fprint(out, prompt)
	sc := bufio.NewScanner(in)
	return sc.Scan() && strings.TrimSpace(sc.Text()) == want
}

// restoreBlock is empty when the public URL is set and the server is not answering.
func restoreBlock(publicURL string, up bool) string {
	if strings.TrimSpace(publicURL) == "" {
		return "Set the public URL before restore, so castbot can tell that the server is stopped."
	}
	if up {
		return "Stop castbot before restore."
	}
	return ""
}

func adminRollback(args []string) int {
	fs := flag.NewFlagSet("rollback-runtime", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dry := fs.Bool("dry-run", false, "print the plan without calling the machine API")
	yes := fs.Bool("yes", false, "skip the name prompt")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: castbot admin rollback-runtime <agent> [--dry-run] [--yes]")
		return 2
	}
	cfg, st := openStore()
	defer st.Close()
	ctx, stop := interruptContext()
	defer stop()
	agent, err := st.AgentByIDOrSlug(ctx, fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent not found")
		return 1
	}
	rb, err := st.ActiveRollback(ctx, agent.UserID, agent.ID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			fmt.Fprintln(os.Stderr, "rollback plan failed")
			return 1
		}
		current := ""
		if vm, vmErr := st.LiveVM(ctx, agent.UserID, agent.ID); vmErr == nil {
			current = vm.Image
		}
		writeRollbackPlan(os.Stdout, agent.Name, current, "")
		fmt.Fprintln(os.Stdout, "no runtime rollback is saved")
		return 1
	}
	if *dry {
		return printRollback(ctx, st, agent, rb.Image, os.Stdout)
	}
	if !castbotUp(cfg.PublicURL) {
		fmt.Fprintln(os.Stderr, "castbot is not reachable")
		return 1
	}
	plan, err := replace.DryRun(ctx, st, agent.UserID, agent.ID, rb.Image)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rollback plan failed")
		return 1
	}
	writeRollbackPlan(os.Stdout, plan.AgentName, plan.CurrentImage, plan.TargetImage)
	if plan.Block != "" {
		fmt.Fprintln(os.Stdout, "blocked: "+plan.Block)
		return 1
	}
	if !*yes && !confirmTyped(os.Stdin, os.Stderr, "Type "+agent.Name+" to roll back this runtime: ", agent.Name) {
		fmt.Fprintln(os.Stderr, "The name did not match.")
		return 1
	}
	if cfg.MachineToken == "" || cfg.PublicURL == "" {
		fmt.Fprintln(os.Stderr, "No machine host is configured.")
		return 1
	}
	m, err := newMachineHost(host.Options{Token: cfg.MachineToken})
	if err != nil {
		fmt.Fprintln(os.Stderr, "machine host did not start")
		return 1
	}
	return runReplace(ctx, st, m, replace.Options{
		UserID: agent.UserID, AgentID: agent.ID,
		ActorKind: "cli", ActorID: "cli", Actor: "cli",
		Image: rb.Image, PublicURL: cfg.PublicURL,
		DataDir: dataDir(cfg.DBPath),
		Progress: func(title string) {
			fmt.Fprintln(os.Stdout, title)
		},
	}, os.Stdout)
}

func printRollback(ctx context.Context, st *store.Store, agent store.Agent, image string, out io.Writer) int {
	plan, err := replace.DryRun(ctx, st, agent.UserID, agent.ID, image)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rollback plan failed")
		return 1
	}
	writeRollbackPlan(out, plan.AgentName, plan.CurrentImage, plan.TargetImage)
	if plan.Block != "" {
		fmt.Fprintln(out, "blocked: "+plan.Block)
		return 1
	}
	return 0
}

func writeRollbackPlan(out io.Writer, name, current, target string) {
	fmt.Fprintf(out, "agent: %s\n", name)
	fmt.Fprintf(out, "image: %s\n", replace.ImageTag(current))
	if strings.TrimSpace(target) == "" {
		fmt.Fprintln(out, "rollback: none")
		return
	}
	fmt.Fprintf(out, "rollback: %s\n", replace.ImageTag(target))
}

func adminBackup(args []string) int {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: castbot admin backup")
		return 2
	}
	cfg, st := openStore()
	defer st.Close()
	res, err := backup.Create(context.Background(), st, dataDir(cfg.DBPath), time.Now())
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			fmt.Fprintln(os.Stderr, "A backup for this minute already exists.")
			return 1
		}
		fmt.Fprintln(os.Stderr, "backup failed")
		return 1
	}
	fmt.Fprintf(os.Stdout, "backup %s sha256=%s schema_version=%d files=%d\n", res.ID, res.SHA256, res.Schema, res.Files)
	return 0
}

func adminBackups(args []string) int {
	fs := flag.NewFlagSet("backups", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: castbot admin backups")
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "backup list failed")
		return 1
	}
	rows, err := backup.List(dataDir(cfg.DBPath))
	if err != nil {
		fmt.Fprintln(os.Stderr, "backup list failed")
		return 1
	}
	if len(rows) == 0 {
		fmt.Fprintln(os.Stdout, "no backups")
		return 0
	}
	for _, row := range rows {
		if !row.OK {
			fmt.Fprintf(os.Stdout, "%s  unreadable\n", row.ID)
			continue
		}
		fmt.Fprintf(os.Stdout, "%s  schema %d  files %d\n", row.ID, row.Schema, row.Files)
	}
	return 0
}

func adminRestore(args []string) int {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	yes := fs.Bool("yes", false, "skip the backup id prompt")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: castbot admin restore <backup id> [--yes]")
		return 2
	}
	id := fs.Arg(0)
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "restore failed")
		return 1
	}
	if line := restoreBlock(cfg.PublicURL, castbotUp(cfg.PublicURL)); line != "" {
		fmt.Fprintln(os.Stderr, line)
		return 1
	}
	if !*yes && !confirmTyped(os.Stdin, os.Stderr, "Type "+id+" to restore this backup: ", id) {
		fmt.Fprintln(os.Stderr, "The id did not match.")
		return 1
	}
	if err := backup.Restore(dataDir(cfg.DBPath), id); err != nil {
		fmt.Fprintln(os.Stderr, "restore failed")
		return 1
	}
	fmt.Fprintf(os.Stdout, "restored %s\n", id)
	return 0
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
