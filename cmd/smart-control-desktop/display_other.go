//go:build !linux

package main

// prepareDisplay is only needed for WebKitGTK on Linux.
func prepareDisplay() (zoom float64) { return 1 }

func floatOnActiveWorkspace() {}
