package web

import (
	"errors"
	"os"
)

var (
	errNotFound = errors.New("not found")
	errExists   = errors.New("already exists")
)

func isNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, errNotFound)
}
