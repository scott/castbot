"""Keep file tools inside the workspace and route commands through castbot-toolrun.

The pinned Cast runtime starts a command with the runtime environment and
opens any absolute path. This module is loaded from sitecustomize before
that runtime imports its tools. Commands stay attached to the helper so the
runtime can stop them. File tools read and list through the helper, which
opens the path without following a swapped symlink. This file does not copy
secret values into a command.
"""

from __future__ import annotations

import os
import signal
import sys
import threading

_HELPER = os.environ.get("CASTBOT_TOOLRUN", "/usr/local/bin/castbot-toolrun")
_BLOCKED = ("/proc", "/sys", "/dev", "/app/.cast", "/app/.castlet")
_INSTALLED = False
_SPAWN_DEPTH = threading.local()


def roots():
    """Agent directory, install prefix, per-agent temp, and CASTBOT_READ_ROOTS."""
    found = []
    for key in ("CAST_AGENT_PATH", "CAST_AGENT_DIR"):
        value = os.environ.get(key, "").strip()
        if value:
            found.append(value)
    if not found:
        found.append("/app/agent")
    extra = os.environ.get("CASTBOT_READ_ROOTS", "").strip()
    if extra:
        found.extend(part for part in extra.split(":") if part)
    else:
        found.extend(["/app/.local", "/app/.agent-tmp"])
    return found


def _blocked(path):
    path = os.path.abspath(path)
    for prefix in _BLOCKED:
        if path == prefix or path.startswith(prefix + os.sep):
            return True
    return False


def _inside(root, path):
    root = os.path.abspath(root)
    path = os.path.abspath(path)
    if path == root:
        return True
    try:
        return os.path.commonpath([root, path]) == root
    except ValueError:
        return False


def allow_path(file_path, allowed):
    """Absolute path inside a root, after symlinks. Raises OSError otherwise."""
    if not isinstance(file_path, str) or file_path == "" or "\x00" in file_path or "\n" in file_path or "\r" in file_path:
        raise OSError("path refused")
    cleaned = []
    for root in allowed:
        if not isinstance(root, str) or root == "" or "\x00" in root:
            continue
        if not os.path.isabs(root):
            continue
        real = os.path.realpath(root)
        if _blocked(real):
            continue
        if real not in cleaned:
            cleaned.append(real)
    if not cleaned:
        raise OSError("path refused")
    raw = file_path
    if not os.path.isabs(raw):
        raw = os.path.join(cleaned[0], raw)
    resolved = os.path.realpath(raw)
    if _blocked(resolved):
        raise OSError("path refused")
    for root in cleaned:
        if _inside(root, resolved):
            return resolved
    raise OSError("path refused")


def _helper():
    return os.environ.get("CASTBOT_TOOLRUN", _HELPER)


def _is_helper(argv0):
    if not isinstance(argv0, str) or argv0 == "":
        return False
    return os.path.basename(argv0) == "castbot-toolrun" or argv0 == _helper()


def _in_spawn():
    return getattr(_SPAWN_DEPTH, "n", 0) > 0


def _enter_spawn():
    _SPAWN_DEPTH.n = getattr(_SPAWN_DEPTH, "n", 0) + 1


def _leave_spawn():
    _SPAWN_DEPTH.n = max(0, getattr(_SPAWN_DEPTH, "n", 1) - 1)


def _patch_subprocess():
    import subprocess

    real = subprocess.Popen

    class IsolatedPopen(real):
        def __init__(self, args, *popenargs, **kwargs):
            shell = kwargs.pop("shell", False)
            kwargs.pop("preexec_fn", None)
            kwargs.pop("user", None)
            kwargs.pop("group", None)
            kwargs.pop("extra_groups", None)
            explicit = "env" in kwargs and kwargs.get("env") is not None
            if explicit:
                env = dict(kwargs["env"])
                env["CASTBOT_TOOL_EXPLICIT"] = "1"
                kwargs["env"] = env
            helper = _helper()
            if isinstance(args, (list, tuple)) and args and _is_helper(str(args[0])):
                argv = list(args)
            elif shell:
                if isinstance(args, (list, tuple)):
                    script = args[0] if args else ""
                else:
                    script = "" if args is None else str(args)
                argv = [helper, "--", "/bin/bash", "-c", script]
            elif isinstance(args, str):
                argv = [helper, "--", args]
            else:
                argv = [helper, "--", *list(args or [])]
            kwargs["shell"] = False
            _enter_spawn()
            try:
                super().__init__(argv, *popenargs, **kwargs)
            finally:
                _leave_spawn()

        def terminate(self):
            if self.poll() is not None:
                return
            try:
                super().send_signal(signal.SIGTERM)
            except ProcessLookupError:
                return

        def kill(self):
            # The helper is the process the runtime can signal. It stops the
            # command group. A direct kill would remove the helper and leave
            # the group running.
            self.terminate()

    subprocess.Popen = IsolatedPopen


def _refuse_exec(*_args, **_kwargs):
    raise OSError("command refused")


def _wrap_spawn(real):
    def wrapped(path, argv, env, *args, **kwargs):
        if _in_spawn() or real is None:
            return real(path, argv, env, *args, **kwargs)
        helper = _helper()
        tail = list(argv[1:]) if argv else []
        new_argv = [helper, "--", path, *tail]
        return real(helper, new_argv, env, *args, **kwargs)

    return wrapped


def _patch_os():
    import subprocess

    def _system(command):
        return subprocess.call(command, shell=True)

    os.system = _system
    for name in ("execve", "execv", "execvp", "execvpe"):
        if hasattr(os, name):
            setattr(os, name, _refuse_exec)
    # Python 3.13 starts subprocesses with posix_spawn. Refusing that call
    # would refuse the helper itself. Direct calls are sent through the helper.
    for name in ("posix_spawn", "posix_spawnp"):
        real = getattr(os, name, None)
        if real is not None:
            setattr(os, name, _wrap_spawn(real))


def _file_bytes(path):
    import subprocess

    proc = subprocess.run(
        [_helper(), "--file", "read", "--", path],
        capture_output=True,
        timeout=30,
    )
    if proc.returncode != 0:
        raise OSError("path refused")
    return proc.stdout


def _patch_paths(mod):
    def resolve(file_path):
        return allow_path(file_path, roots())

    mod.resolve_agent_file = resolve


def _patch_read(mod):
    tool = getattr(mod, "read_tool", None)
    fn = getattr(tool, "func", None)
    if not callable(fn):
        return

    def wrapped(file_path, offset=0, limit=100):
        try:
            raw = _file_bytes(file_path)
            text = raw.decode("utf-8", errors="replace")
        except (OSError, UnicodeError) as exc:
            return "Error reading file: " + str(exc)
        lines = text.splitlines(keepends=True)
        start = offset if isinstance(offset, int) and offset > 0 else 0
        count = limit if isinstance(limit, int) and limit > 0 else 100
        end = min(start + count, len(lines))
        content = "".join(lines[start:end])
        if end < len(lines):
            content += "\n... (%d more lines)" % (len(lines) - end)
        return content

    tool.func = wrapped


def _patch_glob(mod):
    tool = getattr(mod, "glob_tool", None)
    if tool is None or not hasattr(tool, "func"):
        return

    def wrapped(pattern):
        if not isinstance(pattern, str) or os.path.isabs(pattern) or pattern.startswith("~"):
            return "Error: pattern is outside the workspace"
        if ".." in pattern.split("/"):
            return "Error: pattern is outside the workspace"
        import subprocess

        proc = subprocess.run(
            [_helper(), "--file", "glob", "--", pattern],
            capture_output=True,
            text=True,
            timeout=30,
        )
        if proc.returncode != 0:
            return "No files found matching pattern"
        names = [line for line in proc.stdout.splitlines() if line]
        if not names:
            return "No files found matching pattern"
        return "\n".join(names)

    tool.func = wrapped


def _patch_file_tool(mod):
    for name in dir(mod):
        obj = getattr(mod, name)
        fn = getattr(obj, "func", None)
        if not callable(fn):
            continue

        def wrapped(*args, **kwargs):
            # File changes are not opened in the runtime. A later tool can
            # use the helper; until then the call is refused.
            return "Error: path refused"

        obj.func = wrapped


def _patch_agent_config(mod):
    def _load(self):
        protected = os.path.join(os.path.dirname(os.path.abspath(self.path)), ".cast", "agent.yaml")
        config_path = protected if os.path.isfile(protected) else os.path.join(self.path, "agent.yaml")
        if not os.path.isfile(config_path):
            return {}
        import yaml

        try:
            with open(config_path, "r", encoding="utf-8") as handle:
                text = handle.read()
            loaded = yaml.safe_load(text)
            version = mod.read_agent_yaml_version(text).version
        except yaml.YAMLError as exc:
            raise mod.AgentConfigError(str(exc)) from exc
        if loaded is None:
            loaded = {}
        if not isinstance(loaded, dict):
            raise mod.AgentConfigError("agent.yaml could not be validated")
        loaded["version"] = version
        return loaded

    if hasattr(mod, "AgentConfig"):
        mod.AgentConfig._load_config = _load


_PATCHERS = {
    "cast.tools.paths": _patch_paths,
    "cast.tools.glob": _patch_glob,
    "cast.tools.read": _patch_read,
    "cast.tools.write": _patch_file_tool,
    "cast.tools.edit": _patch_file_tool,
    "cast.tools.list": _patch_file_tool,
    "cast.agent.base": _patch_agent_config,
}


def _after_import(fullname, mod):
    patch = _PATCHERS.get(fullname)
    if patch is not None:
        patch(mod)
    if fullname == "cast.tools.read":
        paths = sys.modules.get("cast.tools.paths")
        if paths is not None and hasattr(paths, "resolve_agent_file"):
            mod.resolve_agent_file = paths.resolve_agent_file


class _Hook:
    def find_spec(self, fullname, path=None, target=None):
        if fullname not in _PATCHERS and fullname != "cast.tools.read":
            return None
        sys.meta_path.remove(self)
        try:
            spec = None
            for finder in sys.meta_path:
                found = getattr(finder, "find_spec", None)
                if found is None:
                    continue
                spec = found(fullname, path, target)
                if spec is not None:
                    break
        finally:
            if self not in sys.meta_path:
                sys.meta_path.insert(0, self)
        if spec is None or spec.loader is None or not hasattr(spec.loader, "exec_module"):
            return None
        loader = spec.loader
        original = loader.exec_module

        def exec_module(module, _orig=original, _name=fullname):
            _orig(module)
            _after_import(_name, module)

        loader.exec_module = exec_module
        return spec


def _patch_loaded():
    for name, patch in _PATCHERS.items():
        mod = sys.modules.get(name)
        if mod is not None:
            patch(mod)
    read = sys.modules.get("cast.tools.read")
    paths = sys.modules.get("cast.tools.paths")
    if read is not None and paths is not None and hasattr(paths, "resolve_agent_file"):
        read.resolve_agent_file = paths.resolve_agent_file


def install():
    """Patch process start and file tools. A second call does nothing."""
    global _INSTALLED
    if _INSTALLED:
        return
    _patch_subprocess()
    _patch_os()
    if not any(isinstance(hook, _Hook) for hook in sys.meta_path):
        sys.meta_path.insert(0, _Hook())
    _patch_loaded()
    _INSTALLED = True


def _selftest():
    import tempfile

    with tempfile.TemporaryDirectory() as root:
        note = os.path.join(root, "note.txt")
        with open(note, "w", encoding="utf-8") as fh:
            fh.write("workspace-ok")
        os.symlink("/etc/passwd", os.path.join(root, "escape"))
        os.symlink("/proc/self/environ", os.path.join(root, "envlink"))
        got = allow_path("note.txt", [root])
        if not got.endswith("/note.txt"):
            raise SystemExit("note path")
        with open(got, encoding="utf-8") as fh:
            if fh.read() != "workspace-ok":
                raise SystemExit("note body")
        for path in (
            "/proc/self/environ",
            "/sys/kernel",
            "/dev/null",
            "/etc/passwd",
            os.path.join(root, "escape"),
            os.path.join(root, "envlink"),
            os.path.join(root, "..", "..", "etc", "passwd"),
        ):
            try:
                allow_path(path, [root])
            except OSError:
                continue
            raise SystemExit("allowed " + path)
        missing = allow_path("missing.txt", [root])
        if not missing.endswith("/missing.txt"):
            raise SystemExit("missing")
    if not callable(getattr(os, "posix_spawn", None)) and not hasattr(os, "posix_spawn"):
        pass
    print("tooliso-ok")


if __name__ == "__main__":
    try:
        _selftest()
    except SystemExit:
        raise
    except Exception as exc:
        print("tooliso-fail", exc, file=sys.stderr)
        raise SystemExit(1)
