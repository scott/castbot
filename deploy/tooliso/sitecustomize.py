"""Loaded by Python before the Cast runtime. Commands stay on the helper."""

try:
    import castbot_tooliso

    castbot_tooliso.install()
except Exception:
    import subprocess

    def _refuse(*_args, **_kwargs):
        raise OSError("command refused")

    subprocess.Popen = _refuse
