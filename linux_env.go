//go:build linux

package main

import "os"

func init() {
	// Ubuntu 24.04+ blocks bubblewrap for unprivileged apps unless an AppArmor
	// profile is installed. WebKitGTK uses bwrap for its web-process sandbox and
	// crashes the first time a WebView window is created without this override.
	for _, entry := range [][2]string{
		{"WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS", "1"},
		{"WEBKIT_FORCE_SANDBOX", "0"},
	} {
		if os.Getenv(entry[0]) == "" {
			_ = os.Setenv(entry[0], entry[1])
		}
	}
}
