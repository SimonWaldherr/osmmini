//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd

package osmmini

import "os"

func mapGraphFile(path string) ([]byte, func() error, error) {
	data, err := os.ReadFile(path)
	return data, func() error { return nil }, err
}
