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

// procTCP and procTCP6 are the kernel tables that record a socket's owner.
// Tests point them at a fixture.
var (
	procTCP  = "/proc/net/tcp"
	procTCP6 = "/proc/net/tcp6"
)

// sameUserPeer reports whether the caller is this process's user.
// The lookup uses the caller side of the socket. A missing row, a parse
// error, or any other user is refused.
func sameUserPeer(r *http.Request) bool {
	uid, ok := socketUID(r)
	if !ok {
		return false
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
	if lip == nil || rip == nil {
		return 0, false
	}
	// The caller row lists the peer as local and this server as remote.
	var table, wantLocal, wantRemote string
	if lip.To4() != nil && rip.To4() != nil {
		table = procTCP
		wantLocal = hexIPv4(rip) + ":" + hexPort(rport)
		wantRemote = hexIPv4(lip) + ":" + hexPort(lport)
	} else {
		table = procTCP6
		wantLocal = hexIPv6(rip) + ":" + hexPort(rport)
		wantRemote = hexIPv6(lip) + ":" + hexPort(lport)
	}
	return uidFromProc(table, wantLocal, wantRemote)
}

func hexIPv4(ip net.IP) string {
	ip = ip.To4()
	return fmt.Sprintf("%02X%02X%02X%02X", ip[3], ip[2], ip[1], ip[0])
}

func hexIPv6(ip net.IP) string {
	ip = ip.To16()
	var b strings.Builder
	for i := 0; i < 16; i += 4 {
		fmt.Fprintf(&b, "%02X%02X%02X%02X", ip[i+3], ip[i+2], ip[i+1], ip[i])
	}
	return b.String()
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
