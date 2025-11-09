package main

import (
	"fmt"
	"runtime"

	"ova-esxi-uploader/cmd"
)

var (
	Version   = "dev"
	BuildTime = "unknown"
	GitCommit = "unknown"
)

func init() {
	// Set version info that can be injected at build time
	if Version != "dev" {
		fmt.Printf("OVA ESXi Uploader v%s\n", Version)
		fmt.Printf("Built: %s\n", BuildTime)
		fmt.Printf("Commit: %s\n", GitCommit)
		fmt.Printf("Go: %s\n", runtime.Version())
		fmt.Printf("Platform: %s/%s\n\n", runtime.GOOS, runtime.GOARCH)
	}
}

func main() {
	cmd.Execute()
}
