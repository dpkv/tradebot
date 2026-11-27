//go:build !darwin

package subcmds

import "github.com/bvk/tradebot/exchange"

// defaultDisplayRunner returns the display runner to use when no Xvnc server
// is requested with the --xvnc-display flag. Outside macOS a display server
// is needed for GUI commands, so there is no default.
func defaultDisplayRunner() exchange.DisplayRunner {
	return nil
}
