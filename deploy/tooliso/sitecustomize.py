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


import errno

_RAW_OPEN = os.open
_RAW_MKDIR = os.mkdir

try:
    import ctypes

    _LIBC = ctypes.CDLL(None, use_errno=True)
except Exception:
    _LIBC = None

# Linux openat2. The numbers match the kernel on the architectures this
# image is built for. A missing call falls back to a no-follow walk.
_SYS_OPENAT2 = 437
_RESOLVE_NO_MAGICLINKS = 0x02
_RESOLVE_NO_SYMLINKS = 0x04
_RESOLVE_BENEATH = 0x08


if _LIBC is not None:
    class _OpenHow(ctypes.Structure):
        _fields_ = [
            ("flags", ctypes.c_uint64),
            ("mode", ctypes.c_uint64),
            ("resolve", ctypes.c_uint64),
            ("reserved", ctypes.c_uint64 * 7),
        ]

    _LIBC.syscall.restype = ctypes.c_long


def _agent_root():
    raw = os.environ.get("CAST_AGENT_DIR") or "/app/agent"
    try:
        root = os.path.realpath(raw)
    except OSError:
        return ""
    if root == "" or not os.path.isdir(root):
        return ""
    return root


def _write_rel(path):
    """Relative path of a write that stays inside the agent tree.

    None means the path is not in that tree. OSError means the path aimed
    at the tree and then left it, or a component is empty.
    """
    root = _agent_root()
    if root == "" or path is None:
        return None
    text = os.fspath(path)
    if text == "":
        return None
    if not os.path.isabs(text):
        text = os.path.join(os.getcwd(), text)
    aimed = text == root or text.startswith(root + os.sep)
    norm = os.path.normpath(text)
    inside = norm == root or norm.startswith(root + os.sep)
    if aimed and not inside:
        raise OSError(errno.EPERM, "path")
    if not inside:
        return None
    if norm == root:
        return ""
    rel = norm[len(root) + 1:]
    parts = rel.split(os.sep)
    if any(part in ("", ".", "..") for part in parts):
        raise OSError(errno.EPERM, "path")
    return rel


def _openat2(dirfd, rel, flags, mode):
    if _LIBC is None:
        raise OSError(errno.ENOSYS, "openat2")
    how = _OpenHow(
        flags=flags,
        mode=mode,
        resolve=_RESOLVE_NO_MAGICLINKS | _RESOLVE_NO_SYMLINKS | _RESOLVE_BENEATH,
    )
    fd = _LIBC.syscall(
        ctypes.c_long(_SYS_OPENAT2),
        ctypes.c_int(dirfd),
        ctypes.c_char_p(os.fsencode(rel)),
        ctypes.byref(how),
        ctypes.c_size_t(ctypes.sizeof(how)),
    )
    if fd < 0:
        err = ctypes.get_errno()
        raise OSError(err, os.strerror(err), rel)
    return fd


def _root_fd():
    return _RAW_OPEN(_agent_root(), os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC)


def _open_under(rel, flags, mode):
    """Open rel beneath the agent root without following a symlink."""
    rootfd = _root_fd()
    try:
        try:
            return _openat2(rootfd, rel, flags | os.O_CLOEXEC | os.O_NOFOLLOW, mode)
        except OSError as exc:
            if exc.errno != errno.ENOSYS:
                raise
        return _open_walk(rootfd, rel, flags, mode)
    finally:
        os.close(rootfd)


def _open_walk(rootfd, rel, flags, mode):
    dirfd = os.dup(rootfd)
    try:
        parts = rel.split(os.sep)
        for index, part in enumerate(parts):
            last = index == len(parts) - 1
            open_flags = (flags if last else os.O_RDONLY | os.O_DIRECTORY) | os.O_NOFOLLOW | os.O_CLOEXEC
            nxt = _RAW_OPEN(part, open_flags, mode if last else 0, dir_fd=dirfd)
            os.close(dirfd)
            dirfd = nxt
            if last:
                return dirfd
    except Exception:
        os.close(dirfd)
        raise
    os.close(dirfd)
    raise OSError(errno.EINVAL, "path")


def _castwork_gid():
    try:
        import grp

        return grp.getgrnam("castwork").gr_gid
    except Exception:
        return None


def _share_fd(fd, directory):
    mode = 0o2770 if directory else 0o660
    os.fchmod(fd, mode)
    gid = _castwork_gid()
    if gid is None:
        return
    try:
        os.fchown(fd, -1, gid)
    except OSError:
        pass


def _shared_group(gid):
    """True when gid is the castwork group.

    Without that group, the process group is the shared group.
    """
    named = _castwork_gid()
    if named is None:
        return gid == os.getgid()
    return gid == named


def _prepare_write(fd, truncate):
    """Finish a write fd before any truncation.

    A file this process owns becomes mode 0660. A file someone else owns
    is left unchanged when it is already group-writable by the shared
    group. Anything else is refused, and the file is not shortened.
    """
    import stat

    st = os.fstat(fd)
    if st.st_uid == os.geteuid():
        _share_fd(fd, False)
    elif stat.S_IMODE(st.st_mode) & 0o020 and _shared_group(st.st_gid):
        pass
    else:
        raise OSError(errno.EPERM, "mode")
    if truncate:
        os.ftruncate(fd, 0)


def _create_mode(flags, mode):
    creat = os.O_CREAT | getattr(os, "O_TMPFILE", 0)
    if flags & creat:
        return mode
    return 0


def _mkdir_under(rel):
    parent_rel, name = os.path.split(rel)
    parent_fd = _root_fd() if parent_rel in ("", ".") else _open_under(parent_rel, os.O_RDONLY | os.O_DIRECTORY, 0)
    try:
        _RAW_MKDIR(name, 0o770, dir_fd=parent_fd)
        child = _RAW_OPEN(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW | os.O_CLOEXEC, dir_fd=parent_fd)
    finally:
        os.close(parent_fd)
    try:
        _share_fd(child, True)
    finally:
        os.close(child)


def _writing_mode(mode):
    return isinstance(mode, str) and any(ch in mode for ch in "wax+")


def _writing_flags(flags):
    if flags & (os.O_DIRECTORY | getattr(os, "O_PATH", 0)):
        return False
    access = flags & os.O_ACCMODE
    return access in (os.O_WRONLY, os.O_RDWR) or bool(flags & (os.O_CREAT | os.O_TRUNC | os.O_APPEND))


def _share_agent_writes():
    import builtins
    import io

    def shared_mkdir(path, mode=0o777, *args, dir_fd=None, **kwargs):
        if dir_fd is None:
            rel = _write_rel(path)
            if rel not in (None, ""):
                _mkdir_under(rel)
                return None
        return _RAW_MKDIR(path, mode, *args, dir_fd=dir_fd, **kwargs)

    os.mkdir = shared_mkdir

    def shared_open(file, mode="r", buffering=-1, encoding=None, errors=None, newline=None, closefd=True, opener=None):
        if isinstance(file, (str, os.PathLike)) and _writing_mode(mode):
            rel = _write_rel(file)
            if rel not in (None, ""):
                flags = os.O_CLOEXEC
                truncate = False
                if "w" in mode or "a" in mode or "x" in mode:
                    flags |= os.O_CREAT
                if "w" in mode:
                    truncate = True
                if "x" in mode:
                    flags |= os.O_EXCL
                if "a" in mode:
                    flags |= os.O_APPEND
                if "+" in mode:
                    flags |= os.O_RDWR
                else:
                    flags |= os.O_WRONLY
                fd = _open_under(rel, flags, _create_mode(flags, 0o660))
                try:
                    _prepare_write(fd, truncate)
                    return os.fdopen(fd, mode, buffering, encoding, errors, newline)
                except Exception:
                    os.close(fd)
                    raise
        return builtins_open(file, mode, buffering, encoding, errors, newline, closefd, opener)

    builtins_open = builtins.open
    builtins.open = shared_open
    io.open = shared_open

    def shared_os_open(path, flags, mode=0o777, *, dir_fd=None):
        if dir_fd is None and _writing_flags(flags):
            rel = _write_rel(path)
            if rel not in (None, ""):
                truncate = bool(flags & os.O_TRUNC)
                flags = flags & ~os.O_TRUNC
                fd = _open_under(rel, flags, _create_mode(flags, 0o660))
                try:
                    _prepare_write(fd, truncate)
                except Exception:
                    os.close(fd)
                    raise
                return fd
        return _RAW_OPEN(path, flags, mode, dir_fd=dir_fd)

    os.open = shared_os_open


_share_agent_writes()
