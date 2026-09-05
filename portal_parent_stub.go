//go:build !(linux && cgo)

package main

import "errors"

// acquirePortalParentWindow is implemented with GTK in portal_parent_linux.go
// when building on Linux with CGO. Other platforms (and Linux without CGO)
// get this stub so capture_service.go still compiles.
func acquirePortalParentWindow() (parentWindow string, cleanup func(), err error) {
	return "", nil, errors.New("portal parent window export requires Linux with CGO")
}
