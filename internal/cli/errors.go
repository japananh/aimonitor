package cli

import "fmt"

func errNotImplemented(cmd string) error {
	return fmt.Errorf("%q is not implemented yet (v1.0.0-beta is in development)", cmd)
}
