//go:build unix

package config

import (
	"os"
	"syscall"
)

// checkSafe rejects a file that is not owned by root or the effective
// user, or that group or others may write.
func checkSafe(f *os.File) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return ErrUnsafe
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != 0 && int(st.Uid) != os.Geteuid() {
		return ErrUnsafe
	}
	return nil
}
