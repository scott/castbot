//go:build !linux

package castlet

import "net/http"

// sameUserPeer refuses the call on a build that cannot see the caller.
func sameUserPeer(r *http.Request) bool { return false }

func (s *Supervisor) runtimePeer(r *http.Request) bool { return false }
