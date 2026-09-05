//go:build linux && !cgo

package main

import "errors"

func acquirePortalParentWindow() (parentWindow string, cleanup func(), err error) {
	return "", nil, errors.New("portal parent window export requires CGO")
}
