//go:build !((linux || freebsd || openbsd || netbsd) && !android)

package notify

import "errors"

func newDesktop() (Desktop, error) {
	return nil, errors.New("not supported on this platform yet")
}
