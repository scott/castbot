"""Keep file tools inside the workspace and route commands through castbot-toolrun.

The pinned Cast runtime starts a command with the runtime environment and
opens any absolute path. This module is loaded from sitecustomize before
that runtime imports its tools. Command isolation is the setuid helper.
This file does not copy secret values into a command.
"""

from __future__ import annotations

import glob
import os
import sys

_HELPER = os.environ.get("CASTBOT_TOOLRUN", "/usr/local/bin/castbot-toolrun")
_BLOCKED = ("/proc", "/sys", "/dev", "/app/.cast", "/app/.castlet")
_INSTALLED = False


def roots():
    """Agent directory, install prefix, temp, and CASTBOT_READ_ROOTS."""
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
        found.extend(["/app/.local", "/tmp"])
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
            helper = os.environ.get("CASTBOT_TOOLRUN", _HELPER)
            if shell:
                if isinstance(args, (list, tuple)):
                    script = args[0] if args else ""
                else:
                    script = "" if args is None else str(args)
                argv = [helper, "--", "/bin/bash", "-c", script]
            elif isinstance(args, str):
                argv = [helper, "--", args]
            else:
                argv = [helper, "--", *list(args)]
            kwargs["shell"] = False
            super().__init__(argv, *popenargs, **kwargs)

    subprocess.Popen = IsolatedPopen


def _refuse_exec(*_args, **_kwargs):
    raise OSError("command refused")


def _patch_os():
    import subprocess

    def _system(command):
        return subprocess.call(command, shell=True)

    os.system = _system
    for name in ("execve", "execv", "execvp", "execvpe", "posix_spawn", "posix_spawnp"):
        if hasattr(os, name):
            setattr(os, name, _refuse_exec)


def _patch_paths(mod):
    def resolve(file_path):
        return allow_path(file_path, roots())

    mod.resolve_agent_file = resolve


def _patch_glob(mod):
    tool = getattr(mod, "glob_tool", None)
    if tool is None or not hasattr(tool, "func"):
        return

    def wrapped(pattern):
        if not isinstance(pattern, str) or os.path.isabs(pattern) or pattern.startswith("~"):
            return "Error: pattern is outside the workspace"
        if ".." in pattern.split("/"):
            return "Error: pattern is outside the workspace"
        agent = getattr(mod, "agent_root", None)
        root = agent() if callable(agent) else None
        if not root:
            root = os.environ.get("CAST_AGENT_PATH") or os.environ.get("CAST_AGENT_DIR") or "/app/agent"
        try:
            root = allow_path(root, [root])
        except OSError:
            return "Error: pattern is outside the workspace"
        matches = glob.glob(pattern, root_dir=root, recursive=True)
        kept = []
        for match in matches:
            full = match if os.path.isabs(match) else os.path.join(root, match)
            try:
                allow_path(full, [root])
            except OSError:
                continue
            kept.append(match)
        if not kept:
            return "No files found matching pattern"
        return "\n".join(kept)

    tool.func = wrapped


def _patch_file_tool(mod):
    for name in dir(mod):
        obj = getattr(mod, name)
        fn = getattr(obj, "func", None)
        if not callable(fn):
            continue

        def wrapped(*args, _fn=fn, **kwargs):
            path = kwargs.get("file_path", kwargs.get("path"))
            if path is None and args:
                path = args[0]
            if isinstance(path, str):
                try:
                    allow_path(path, roots())
                except OSError:
                    return "Error: path refused"
            return _fn(*args, **kwargs)

        obj.func = wrapped


_PATCHERS = {
    "cast.tools.paths": _patch_paths,
    "cast.tools.glob": _patch_glob,
    "cast.tools.write": _patch_file_tool,
    "cast.tools.edit": _patch_file_tool,
    "cast.tools.list": _patch_file_tool,
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
    print("tooliso-ok")


if __name__ == "__main__":
    try:
        _selftest()
    except SystemExit:
        raise
    except Exception as exc:
        print("tooliso-fail", exc, file=sys.stderr)
        raise SystemExit(1)
