// Package host starts the shared runtime image in local Docker.
// S04 adds the MicroVM host. This one never calls DigitalOcean.
package host

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const Image = "castbot-runtime:s03"

// Build builds the local image from the repository root. It does not push.
func Build(ctx context.Context, root string) error {
	cmd := exec.CommandContext(ctx, "docker", "build", "-t", Image, root)
	var errBuf bytes.Buffer
	cmd.Stdout = &errBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		msg := errBuf.String()
		if len(msg) > 800 {
			msg = msg[len(msg)-800:]
		}
		return fmt.Errorf("docker build: %s", strings.TrimSpace(msg))
	}
	return nil
}

// Guest is one running container.
type Guest struct {
	id   string
	base string
}

// RunOpt is the boot environment for one container.
type RunOpt struct {
	ControlURL string
	AgentID    string
	Bootstrap  string
	CAFile     string
	WordDelay  string
}

// Run starts a container on its own bridge network. castlet is published
// on 127.0.0.1 only. The runtime stays on the container's loopback, so
// two guests cannot share a process or a port.
// The bootstrap value is passed to the container and is not included in errors.
func Run(ctx context.Context, opt RunOpt) (*Guest, error) {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	id := "castbot-s03-" + hex.EncodeToString(buf)
	args := []string{
		"run", "--rm", "-d",
		"--name", id,
		"--add-host", "host.docker.internal:host-gateway",
		"-p", "127.0.0.1::8080",
		"-v", opt.CAFile + ":/run/castbot-ca.pem:ro",
		"-e", "CASTBOT_URL=" + opt.ControlURL,
		"-e", "CASTBOT_AGENT_ID=" + opt.AgentID,
		"-e", "CASTBOT_BOOTSTRAP_TOKEN=" + opt.Bootstrap,
		"-e", "CASTBOT_CA_FILE=/run/castbot-ca.pem",
		"-e", "CASTLET_ADDR=:8080",
	}
	if opt.WordDelay != "" {
		args = append(args, "-e", "CAST_STANDIN_WORD_DELAY="+opt.WordDelay)
	}
	args = append(args, Image)
	cmd := exec.CommandContext(ctx, "docker", args...)
	var errBuf bytes.Buffer
	cmd.Stdout = &errBuf
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		msg := strings.ReplaceAll(errBuf.String(), opt.Bootstrap, "[redacted]")
		if len(msg) > 400 {
			msg = msg[len(msg)-400:]
		}
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", id).Run()
		return nil, fmt.Errorf("docker run: %s", strings.TrimSpace(msg))
	}
	base, err := publishedURL(ctx, id)
	if err != nil {
		_ = exec.CommandContext(ctx, "docker", "rm", "-f", id).Run()
		return nil, err
	}
	return &Guest{id: id, base: base}, nil
}

func publishedURL(ctx context.Context, id string) (string, error) {
	var last string
	for i := 0; i < 25; i++ {
		out, err := exec.CommandContext(ctx, "docker", "port", id, "8080").CombinedOutput()
		last = string(out)
		if err == nil {
			for _, line := range strings.Split(last, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "127.0.0.1:") {
					return "http://" + line, nil
				}
			}
		}
		time.Sleep(40 * time.Millisecond)
	}
	return "", fmt.Errorf("docker port: %s", strings.TrimSpace(last))
}

// Published lists the host port mappings. Callers use it to prove the
// runtime port is not published.
func (g *Guest) Published(ctx context.Context) (string, error) {
	if g == nil || g.id == "" {
		return "", errors.New("no container")
	}
	out, err := exec.CommandContext(ctx, "docker", "port", g.id).CombinedOutput()
	if err != nil {
		return "", errors.New("docker port failed")
	}
	return string(out), nil
}

// URL is the castlet base URL on the host.
func (g *Guest) URL() string {
	if g == nil {
		return ""
	}
	return g.base
}

// Logs returns the container's combined output, trimmed so a boot failure
// can be diagnosed without dumping the whole stream.
func (g *Guest) Logs(ctx context.Context) string {
	if g == nil || g.id == "" {
		return ""
	}
	out, _ := exec.CommandContext(ctx, "docker", "logs", g.id).CombinedOutput()
	text := string(out)
	if len(text) > 1500 {
		text = text[len(text)-1500:]
	}
	return strings.TrimSpace(text)
}

// Stop removes the container.
func (g *Guest) Stop(ctx context.Context) {
	if g == nil || g.id == "" {
		return
	}
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", g.id).Run()
}

// Output runs a command in the container and returns combined output.
// Callers must not pass secret values in the command.
func (g *Guest) Output(ctx context.Context, args ...string) (string, error) {
	if g == nil {
		return "", errors.New("no container")
	}
	cmd := exec.CommandContext(ctx, "docker", append([]string{"exec", g.id}, args...)...)
	out, err := cmd.CombinedOutput()
	text := string(out)
	if len(text) > 2000 {
		text = text[len(text)-2000:]
	}
	if err != nil {
		return text, errors.New("docker exec failed")
	}
	return text, nil
}

// CopyFrom copies a path out of the container.
func (g *Guest) CopyFrom(ctx context.Context, containerPath, hostPath string) error {
	cmd := exec.CommandContext(ctx, "docker", "cp", g.id+":"+containerPath, hostPath)
	if err := cmd.Run(); err != nil {
		return errors.New("docker cp failed")
	}
	return nil
}
