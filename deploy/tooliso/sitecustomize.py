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
