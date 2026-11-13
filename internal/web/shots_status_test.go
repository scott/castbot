//go:build shots

package web

import "time"

func init() {
	testStatusRefresh = func(s *Server) {
		s.RunStatusRefresh(s.base, 200*time.Millisecond)
	}
}
