package tooliso

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRuntimeWriteStaysInTheAgentTree(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "deploy", "tooliso"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", "-c", runtimeWriteScript)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+root, "PYTHONDONTWRITEBYTECODE=1")
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != "write-ok\n" {
		t.Fatalf("runtime write: %v\n%s", err, out)
	}
}

const runtimeWriteScript = `
import os, pathlib, stat, sys, tempfile, types

root = tempfile.mkdtemp()
agent = os.path.join(root, "agent")
sealed = os.path.join(root, ".cast")
os.makedirs(agent)
os.makedirs(sealed)
os.environ["CAST_AGENT_DIR"] = agent
import sitecustomize
import castbot_tooliso

planted = os.path.join(sealed, "secret")
with open(planted, "w", encoding="utf-8") as fh:
    fh.write("sealed")
memory = os.path.join(agent, "memory")
os.makedirs(memory)
link = os.path.join(memory, "note.txt")
os.symlink(planted, link)

def refused(label, fn):
    try:
        fn()
    except OSError:
        return
    raise SystemExit("followed " + label)

refused("open", lambda: open(link, "w", encoding="utf-8").write("pwned"))
refused("pathlib", lambda: pathlib.Path(link).write_text("pwned"))
def os_open():
    fd = os.open(link, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o660)
    os.write(fd, b"pwned")
    os.close(fd)
refused("os.open", os_open)
with open(planted, encoding="utf-8") as fh:
    if fh.read() != "sealed":
        raise SystemExit("sealed changed")

note = pathlib.Path(agent) / "workspace" / "made" / "sub" / "via-path.txt"
note.parent.mkdir(parents=True, exist_ok=True)
note.write_text("pathlib")
if stat.S_IMODE(note.stat().st_mode) != 0o660:
    raise SystemExit("pathlib mode %o" % stat.S_IMODE(note.stat().st_mode))
fd = os.open(os.path.join(agent, "workspace", "via-os.txt"), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
os.write(fd, b"osopen")
os.close(fd)
if stat.S_IMODE(os.stat(os.path.join(agent, "workspace", "via-os.txt")).st_mode) != 0o660:
    raise SystemExit("osopen mode")

workspace = os.path.join(agent, "agent.yaml")
with open(workspace, "w", encoding="utf-8") as fh:
    fh.write("name: FromWorkspace\n")
mod = types.ModuleType("agentcfg")
class AgentConfigError(Exception):
    pass
class Version:
    version = 1
class AgentConfig:
    def __init__(self, path):
        self.path = path
mod.AgentConfigError = AgentConfigError
mod.AgentConfig = AgentConfig
mod.read_agent_yaml_version = lambda text: Version()
yaml = types.ModuleType("yaml")
class YAMLError(Exception):
    pass
def safe_load(text):
    if "bad" in text:
        raise YAMLError("bad")
    out = {}
    for line in text.splitlines():
        if ":" in line:
            key, value = line.split(":", 1)
            out[key.strip()] = value.strip()
    return out
yaml.YAMLError = YAMLError
yaml.safe_load = safe_load
sys.modules["yaml"] = yaml
castbot_tooliso._patch_agent_config(mod)
cfg = AgentConfig(agent)
try:
    cfg._load_config()
except AgentConfigError:
    pass
else:
    raise SystemExit("missing seal loaded")
os.makedirs(os.path.join(root, ".cast"), exist_ok=True)
bad = os.path.join(root, ".cast", "agent.yaml")
with open(bad, "w", encoding="utf-8") as fh:
    fh.write("name: bad\n")
try:
    cfg._load_config()
except AgentConfigError:
    pass
else:
    raise SystemExit("invalid seal loaded")
with open(bad, "w", encoding="utf-8") as fh:
    fh.write("name: Sealed\n")
loaded = cfg._load_config()
if loaded.get("name") != "Sealed":
    raise SystemExit("sealed name " + repr(loaded))
os.remove(bad)
os.symlink(workspace, bad)
try:
    cfg._load_config()
except AgentConfigError:
    pass
else:
    raise SystemExit("seal link loaded")
print("write-ok")
`
