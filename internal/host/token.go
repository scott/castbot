package host

import (
	"os"
	"strings"
)

// DOToken is the only machine token this process reads.
// DIGITALOCEAN_ACCESS_TOKEN, DIGITALOCEAN_TOKEN, and any other name are ignored.
func DOToken() string {
	return strings.TrimSpace(os.Getenv("CASTBOT_DO_TOKEN"))
}
