//go:build !unix

package config

import "os"

// checkSafe has no owner information to check outside unix.
func checkSafe(*os.File) error { return nil }
