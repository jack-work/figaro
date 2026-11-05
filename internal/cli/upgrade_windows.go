//go:build windows

package cli

func runUpgrade(o upgradeOptions) {
	_ = o
	die("figaro upgrade needs tmux and /proc; it runs on Linux")
}
