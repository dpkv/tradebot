//go:build darwin

package subcmds

import (
	"os/exec"

	"github.com/bvk/tradebot/exchange"
)

// nativeDisplay runs GUI commands on the macOS desktop. macOS has its own
// window server, so the browser used for E*TRADE automated logins opens on
// the logged-in user's screen without any X11 or Xvnc server.
type nativeDisplay struct{}

func (nativeDisplay) RunWithDisplay(cmd *exec.Cmd) error {
	return cmd.Run()
}

// defaultDisplayRunner returns the display runner to use when no Xvnc server
// is requested with the --xvnc-display flag.
func defaultDisplayRunner() exchange.DisplayRunner {
	return nativeDisplay{}
}
