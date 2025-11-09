package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "ova-esxi-uploader",
	Short: "Robust OVA uploader for ESXi with infinite retry capability",
	Long: `A reliable OVA uploader for ESXi servers that handles network interruptions
gracefully with automatic retry, resume capabilities, and progress tracking.

This tool recreates the functionality of VMware's ovftool but with enhanced
reliability for unstable network connections. It supports:

- Infinite retry with exponential backoff
- Resumable uploads with progress persistence
- Chunked transfers for large files
- Detailed progress tracking and logging
- Checksum validation for data integrity

Example usage:
  ova-esxi-uploader upload vm.ova esxi.example.com --datastore datastore1
  ova-esxi-uploader list-sessions
  ova-esxi-uploader resume --session-id 1699123456`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().BoolP("verbose", "v", false, "Enable verbose logging")
	rootCmd.PersistentFlags().BoolP("quiet", "q", false, "Suppress all output except errors")
}
