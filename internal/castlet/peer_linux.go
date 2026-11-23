//go:build linux

package castlet

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// sameUserPeer reports whether the caller is the castlet user.
// A lookup that cannot see the socket allows the call, so tests without a
// real connection still work. A socket owned by another user is refused.
func sameUserPeer(r *http.Request) bool {
	uid, ok := socketUID(r)
	if !ok {
		return true
	}
	return uid == os.Getuid()
}

func socketUID(r *http.Request) (int, bool) {
	local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || local == nil {
		return 0, false
	}
	lhost, lport, err := net.SplitHostPort(local.String())
	if err != nil {
		return 0, false
	}
	rhost, rport, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return 0, false
	}
	lip := net.ParseIP(lhost)
	rip := net.ParseIP(rhost)
	if lip == nil || rip == nil || lip.To4() == nil || rip.To4() == nil {
		return 0, false
	}
	wantLocal := hexIPv4(lip) + ":" + hexPort(lport)
	wantRemote := hexIPv4(rip) + ":" + hexPort(rport)
	return uidFromProc("/proc/net/tcp", wantLocal, wantRemote)
}

func hexIPv4(ip net.IP) string {
	ip = ip.To4()
	return fmt.Sprintf("%02X%02X%02X%02X", ip[3], ip[2], ip[1], ip[0])
}

func hexPort(port string) string {
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return ""
	}
	return fmt.Sprintf("%04X", n)
}

func uidFromProc(path, local, remote string) (int, bool) {
	if local == "" || remote == "" || !strings.Contains(local, ":") || !strings.Contains(remote, ":") {
		return 0, false
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		if first {
			first = false
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 8 {
			continue
		}
		if !strings.EqualFold(fields[1], local) || !strings.EqualFold(fields[2], remote) {
			continue
		}
		uid, err := strconv.Atoi(fields[7])
		if err != nil {
			return 0, false
		}
		return uid, true
	}
	return 0, false
}
