//go:build darwin || linux || freebsd || openbsd || netbsd

package osmmini

import (
	"os"
	"syscall"
)

func mapGraphFile(path string) ([]byte, func() error, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if stat.Size() < graphHeaderSize {
		return nil, nil, os.ErrInvalid
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(stat.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, nil, err
	}
	return data, func() error { return syscall.Munmap(data) }, nil
}
