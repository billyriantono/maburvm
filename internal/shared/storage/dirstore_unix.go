//go:build unix

package storage

import (
	"fmt"
	"os"
	"syscall"
)

func deviceOf(p string) (uint64, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("no device number for %s", p)
	}
	return uint64(st.Dev), nil
}
