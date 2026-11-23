//go:build linux

package tooliso

import (
	"errors"
	"os"
	"os/exec"
	"os/user"
	"strconv"
	"syscall"
)

// RuntimeUser is the account that runs the Cast runtime.
const RuntimeUser = "nobody"

func credential(name string) (*syscall.Credential, error) {
	account, err := user.Lookup(name)
	if err != nil {
		return nil, err
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil || uid < 0 {
		return nil, errors.New("account")
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil || gid < 0 {
		return nil, errors.New("account")
	}
	return &syscall.Credential{
		Uid:         uint32(uid),
		Gid:         uint32(gid),
		Groups:      []uint32{uint32(gid)},
		NoSetGroups: false,
	}, nil
}

// ApplyRuntime starts cmd as the runtime account when this process is root.
// The group list is only that account's group. A non-root process is unchanged.
func ApplyRuntime(cmd *exec.Cmd) error {
	if cmd == nil || os.Geteuid() != 0 {
		return nil
	}
	cred, err := credential(RuntimeUser)
	if err != nil {
		return err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = cred
	return nil
}

// ApplyTool starts cmd as the tool account when this process is root.
// The child is its own process group and dies with this process.
func ApplyTool(cmd *exec.Cmd) error {
	if cmd == nil || os.Geteuid() != 0 {
		return nil
	}
	cred, err := credential(ToolUser)
	if err != nil {
		return err
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Credential = cred
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
	return nil
}

// OwnRuntime gives path to the runtime account when this process is root.
func OwnRuntime(path string) {
	if os.Geteuid() != 0 || path == "" {
		return
	}
	cred, err := credential(RuntimeUser)
	if err != nil {
		return
	}
	_ = os.Chown(path, int(cred.Uid), int(cred.Gid))
}

// ShareCastwork sets the group of an open file to castwork when this process is root.
func ShareCastwork(fd int) {
	if os.Geteuid() != 0 || fd < 0 {
		return
	}
	grp, err := user.LookupGroup("castwork")
	if err != nil {
		return
	}
	gid, err := strconv.Atoi(grp.Gid)
	if err != nil || gid < 0 {
		return
	}
	_ = syscall.Fchown(fd, -1, gid)
}
