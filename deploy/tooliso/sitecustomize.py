"""Loaded by Python before the Cast runtime. Commands stay on the helper."""

import os

try:
    import ctypes

    ctypes.CDLL(None).prctl(4, 0, 0, 0, 0)
except Exception:
    pass
os.umask(0o077)

try:
    import castbot_tooliso

    castbot_tooliso.install()
except Exception:
    import subprocess

    def _refuse(*_args, **_kwargs):
        raise OSError("command refused")

    subprocess.Popen = _refuse


def _agent_root():
    raw = os.environ.get("CAST_AGENT_DIR") or "/app/agent"
    try:
        return os.path.realpath(raw)
    except OSError:
        return ""


def _share_agent(path, directory):
    """Files and directories the runtime creates under the agent tree stay
    readable by the agent user. Private files outside that tree do not.
    """
    root = _agent_root()
    if root == "" or not isinstance(path, str) or path == "":
        return
    try:
        if os.path.islink(path):
            return
        real = os.path.realpath(path)
    except OSError:
        return
    if real != root and not real.startswith(root + os.sep):
        return
    try:
        os.chmod(path, 0o2770 if directory else 0o660)
    except OSError:
        pass


def _share_agent_writes():
    import builtins

    mkdir = os.mkdir

    def shared_mkdir(path, mode=0o777, *args, **kwargs):
        mkdir(path, mode, *args, **kwargs)
        _share_agent(path if isinstance(path, str) else os.fspath(path), True)

    os.mkdir = shared_mkdir
    builtin_open = builtins.open

    def shared_open(file, mode="r", *args, **kwargs):
        handle = builtin_open(file, mode, *args, **kwargs)
        if isinstance(file, (str, os.PathLike)) and any(ch in mode for ch in "wax+"):
            _share_agent(os.fspath(file), False)
        return handle

    builtins.open = shared_open


_share_agent_writes()
