package boot

import "github.com/jack-work/figaro/internal/auth"

func init() {
	if semver != "" {
		auth.UserAgent = "figaro/" + semver
	}
}
