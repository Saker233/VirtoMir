//go:build !unix

package update

import "errors"

func execSelf(string) error {
	return errors.New("restarting in place is only supported on Unix")
}
