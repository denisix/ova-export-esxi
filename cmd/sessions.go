package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"ova-esxi-uploader/pkg/progress"
)

var listSessionsCmd = &cobra.Command{
	Use:   "list-sessions",
	Short: "List all available upload sessions",
	Long:  `List all available upload sessions that can be resumed.`,
	RunE:  runListSessions,
}

var resumeSessionCmd = &cobra.Command{
	Use:   "resume",
	Short: "Resume a previous upload session",
	Long: `Resume a previous upload session by session ID.
If no session ID is provided, the most recent session will be resumed.`,
	RunE: runResumeSession,
}

var cleanSessionsCmd = &cobra.Command{
	Use:   "clean-sessions",
	Short: "Clean up old upload session files",
	Long:  `Remove all upload session files from the current directory.`,
	RunE:  runCleanSessions,
}

func init() {
	rootCmd.AddCommand(listSessionsCmd)
	rootCmd.AddCommand(resumeSessionCmd)
	rootCmd.AddCommand(cleanSessionsCmd)

	resumeSessionCmd.Flags().StringVar(&sessionID, "session-id", "", "Specific session ID to resume")
}

func runListSessions(cmd *cobra.Command, args []string) error {
	sessions, err := progress.FindExistingSessions(".")
	if err != nil {
		return fmt.Errorf("failed to find sessions: %w", err)
	}

	if len(sessions) == 0 {
		fmt.Println("No upload sessions found.")
		return nil
	}

	fmt.Printf("Found %d upload session(s):\n\n", len(sessions))

	for _, sessionFile := range sessions {
		tracker, err := progress.LoadTracker(sessionFile)
		if err != nil {
			fmt.Printf("❌ %s (failed to load: %v)\n", sessionFile, err)
			continue
		}

		session := tracker.GetSession()

		// Get file modification time
		stat, err := os.Stat(sessionFile)
		var modTime time.Time
		if err == nil {
			modTime = stat.ModTime()
		}

		status := "❌ Failed"
		if session.IsCompleted {
			status = "✅ Completed"
		} else {
			status = "⏸️ In Progress"
		}

		percentage, uploaded, total := tracker.GetOverallProgress()

		fmt.Printf("%s Session ID: %s\n", status, session.SessionID)
		fmt.Printf("   File: %s\n", filepath.Base(session.OVAFile))
		fmt.Printf("   ESXi: %s\n", session.ESXiHost)
		fmt.Printf("   Datastore: %s\n", session.Datastore)
		fmt.Printf("   VM Name: %s\n", session.VMName)
		fmt.Printf("   Progress: %.1f%% (%s / %s)\n", percentage, formatBytes(uploaded), formatBytes(total))
		fmt.Printf("   Files: %d total\n", len(session.Files))

		if !modTime.IsZero() {
			fmt.Printf("   Last Update: %s\n", modTime.Format("2006-01-02 15:04:05"))
		}

		if session.RetryAttempts > 0 {
			fmt.Printf("   Retry Attempts: %d\n", session.RetryAttempts)
		}

		fmt.Printf("   Duration: %s\n", time.Since(session.StartTime).Round(time.Second))
		fmt.Println()

		tracker.Close()
	}

	return nil
}

func runResumeSession(cmd *cobra.Command, args []string) error {
	sessions, err := progress.FindExistingSessions(".")
	if err != nil {
		return fmt.Errorf("failed to find sessions: %w", err)
	}

	if len(sessions) == 0 {
		return fmt.Errorf("no upload sessions found to resume")
	}

	var sessionFile string
	if sessionID != "" {
		// Look for specific session ID
		for _, s := range sessions {
			if containsSessionID(s, sessionID) {
				sessionFile = s
				break
			}
		}
		if sessionFile == "" {
			return fmt.Errorf("session with ID %s not found", sessionID)
		}
	} else {
		// Use the most recent session
		sessionFile = sessions[0]
		for _, s := range sessions[1:] {
			stat1, _ := os.Stat(sessionFile)
			stat2, _ := os.Stat(s)
			if stat2.ModTime().After(stat1.ModTime()) {
				sessionFile = s
			}
		}
	}

	tracker, err := progress.LoadTracker(sessionFile)
	if err != nil {
		return fmt.Errorf("failed to load session: %w", err)
	}

	session := tracker.GetSession()
	tracker.Close()

	if session.IsCompleted {
		fmt.Printf("Session %s is already completed.\n", session.SessionID)
		return nil
	}

	fmt.Printf("Resuming session %s...\n", session.SessionID)
	fmt.Printf("OVA File: %s\n", session.OVAFile)
	fmt.Printf("ESXi Host: %s\n", session.ESXiHost)
	fmt.Printf("Datastore: %s\n", session.Datastore)

	// Call upload command with resume flag
	uploadCmd.Flag("resume").Value.Set("true")
	uploadCmd.Flag("session-id").Value.Set(session.SessionID)

	return runUpload(cmd, []string{session.OVAFile, session.ESXiHost})
}

func runCleanSessions(cmd *cobra.Command, args []string) error {
	sessions, err := progress.FindExistingSessions(".")
	if err != nil {
		return fmt.Errorf("failed to find sessions: %w", err)
	}

	if len(sessions) == 0 {
		fmt.Println("No session files found to clean.")
		return nil
	}

	fmt.Printf("Found %d session file(s) to clean:\n", len(sessions))
	for _, sessionFile := range sessions {
		fmt.Printf("  %s\n", sessionFile)
	}

	fmt.Print("Delete all session files? (y/N): ")
	var response string
	fmt.Scanln(&response)

	if response != "y" && response != "Y" && response != "yes" && response != "Yes" {
		fmt.Println("Cancelled.")
		return nil
	}

	deleted := 0
	for _, sessionFile := range sessions {
		if err := os.Remove(sessionFile); err != nil {
			fmt.Printf("Failed to delete %s: %v\n", sessionFile, err)
		} else {
			deleted++
		}
	}

	fmt.Printf("Successfully deleted %d session file(s).\n", deleted)
	return nil
}

func containsSessionID(filename, sessionID string) bool {
	return filepath.Base(filename) == fmt.Sprintf(".upload-session-%s.json", sessionID)
}
