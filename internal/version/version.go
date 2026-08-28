// Package version carries the build version, stamped via ldflags:
//
//	go build -ldflags "-X github.com/smart-minds/keel/internal/version.Version=v1.2.3"
package version

// Version is the Keel build version.
var Version = "dev"
