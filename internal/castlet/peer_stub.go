//go:build !linux

package castlet

import "net/http"

func sameUserPeer(r *http.Request) bool { return true }
