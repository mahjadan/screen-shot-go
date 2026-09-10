//go:build !(linux && cgo) || gtk3

package main

import "errors"

// acquirePortalParentWindow is implemented with GTK4 in portal_parent_linux.go
// when building on Linux with CGO and the default (GTK4) backend. Other
// platforms, Linux without CGO, and the gtk3-tagged backend (which doesn't
// pull in GTK4 at all) get this stub so capture_service.go still compiles;
// the portal capture path already treats its failure as non-fatal.
func acquirePortalParentWindow() (parentWindow string, cleanup func(), err error) {
	return "", nil, errors.New("portal parent window export requires Linux with CGO")
}
