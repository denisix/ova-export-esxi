package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"ova-esxi-uploader/pkg/esxi"
	"ova-esxi-uploader/pkg/ova"
	"ova-esxi-uploader/pkg/progress"
	"ova-esxi-uploader/pkg/retry"

	"github.com/vmware/govmomi/object"
)

var uploadCmd = &cobra.Command{
	Use:   "upload [OVA_FILE] [ESXI_HOST]",
	Short: "Upload OVA file to ESXi server with infinite retry capability",
	Long: `Upload an OVA file to an ESXi server with robust retry mechanism.
This command will parse the OVA file, connect to ESXi, and upload all components
with automatic retry on network failures.

Examples:
  ova-esxi-uploader upload vm.ova esxi.example.com
  ova-esxi-uploader upload vm.ova esxi.example.com --datastore datastore1
  ova-esxi-uploader upload vm.ova esxi.example.com --vm-name "My VM" --network "VM Network"
  ova-esxi-uploader upload vm.ova esxi.example.com --datastore datastore1 --workers 5 --verbose`,
	Args: cobra.ExactArgs(2),
	RunE: runUpload,
}

var (
	username     string
	password     string
	datastore    string
	vmName       string
	network      string
	insecure     bool
	chunkSize    int64
	maxRetries   int
	baseDelay    time.Duration
	maxDelay     time.Duration
	resume       bool
	sessionID    string
	useStreaming bool
	logFile      string
	workers      int
)

func init() {
	rootCmd.AddCommand(uploadCmd)

	uploadCmd.Flags().StringVarP(&username, "username", "u", "root", "ESXi username")
	uploadCmd.Flags().StringVarP(&password, "password", "p", "", "ESXi password (will prompt if not provided)")
	uploadCmd.Flags().StringVarP(&datastore, "datastore", "d", "", "Target datastore name (required)")
	uploadCmd.Flags().StringVarP(&vmName, "vm-name", "n", "", "Virtual machine name (defaults to OVA filename)")
	uploadCmd.Flags().StringVar(&network, "network", "VM Network", "Network name for VM")
	uploadCmd.Flags().BoolVar(&insecure, "insecure", true, "Skip SSL certificate verification")
	uploadCmd.Flags().Int64Var(&chunkSize, "chunk-size", 32*1024*1024, "Upload chunk size in bytes")
	uploadCmd.Flags().IntVar(&maxRetries, "max-retries", 0, "Maximum retry attempts (0 for infinite)")
	uploadCmd.Flags().DurationVar(&baseDelay, "base-delay", 2*time.Second, "Base delay between retries")
	uploadCmd.Flags().DurationVar(&maxDelay, "max-delay", 2*time.Minute, "Maximum delay between retries")
	uploadCmd.Flags().BoolVar(&resume, "resume", false, "Resume from previous upload session")
	uploadCmd.Flags().StringVar(&sessionID, "session-id", "", "Specific session ID to resume")
	uploadCmd.Flags().BoolVar(&useStreaming, "stream", true, "Use streaming upload (no temp files, faster)")
	uploadCmd.Flags().StringVar(&logFile, "log", "", "Write detailed logs to file (always verbose)")
	uploadCmd.Flags().IntVar(&workers, "workers", 3, "Number of parallel upload workers (1-10)")

	uploadCmd.MarkFlagRequired("datastore")
}

func runUpload(cmd *cobra.Command, args []string) error {
	ovaFile := args[0]
	esxiHost := args[1]

	// Get verbose flag
	verbose, _ := cmd.Flags().GetBool("verbose")
	quiet, _ := cmd.Flags().GetBool("quiet")

	// Setup logger
	logger := logrus.New()
	var fileLogger *logrus.Logger

	// Console logger setup
	if quiet {
		logger.SetLevel(logrus.ErrorLevel)
	} else if verbose {
		logger.SetLevel(logrus.DebugLevel)
	} else {
		logger.SetLevel(logrus.InfoLevel)
	}
	logger.SetFormatter(&logrus.TextFormatter{
		FullTimestamp: true,
	})

	// File logger setup
	if logFile != "" {
		logFileHandle, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			return fmt.Errorf("failed to open log file: %w", err)
		}
		defer logFileHandle.Close()

		fileLogger = logrus.New()
		fileLogger.SetOutput(logFileHandle)
		fileLogger.SetLevel(logrus.DebugLevel) // Always verbose in file
		fileLogger.SetFormatter(&logrus.TextFormatter{
			FullTimestamp: true,
		})

		// Note: verbose flag for console remains unchanged - only file logging is always verbose

		// Also log to file that we're starting
		fileLogger.WithFields(logrus.Fields{
			"ova_file":  ovaFile,
			"esxi_host": esxiHost,
			"datastore": datastore,
			"vm_name":   vmName,
			"log_file":  logFile,
		}).Info("Starting OVA upload with file logging")
	}

	// Check if OVA file exists
	if _, err := os.Stat(ovaFile); os.IsNotExist(err) {
		return fmt.Errorf("OVA file does not exist: %s", ovaFile)
	}

	// Get absolute path for OVA file
	absOVAFile, err := filepath.Abs(ovaFile)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for OVA file: %w", err)
	}

	// Prompt for password if not provided
	if password == "" {
		fmt.Print("Enter ESXi password: ")
		fmt.Scanln(&password)
	}

	// Set VM name if not provided
	if vmName == "" {
		vmName = strings.TrimSuffix(filepath.Base(ovaFile), filepath.Ext(ovaFile))
	}

	// Validate workers parameter
	if workers < 1 || workers > 10 {
		return fmt.Errorf("workers must be between 1 and 10, got %d", workers)
	}

	// Check for existing sessions if resume is requested
	var tracker *progress.Tracker
	if resume {
		sessions, err := progress.FindExistingSessions(".")
		if err != nil {
			logger.WithError(err).Warn("Failed to find existing sessions")
		} else if len(sessions) > 0 {
			var sessionFile string
			if sessionID != "" {
				// Look for specific session ID
				for _, s := range sessions {
					if strings.Contains(s, sessionID) {
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

			tracker, err = progress.LoadTracker(sessionFile)
			if err != nil {
				logger.WithError(err).Warn("Failed to load existing session, starting new upload")
			} else {
				logger.WithField("session", sessionFile).Info("Resuming previous upload session")
			}
		}
	}

	// Create new tracker if none loaded
	if tracker == nil {
		sessionID := fmt.Sprintf("%d", time.Now().Unix())
		tracker = progress.NewTracker(sessionID, absOVAFile, esxiHost, datastore, vmName)
	}

	tracker.SetLogger(logger)

	// Parse OVA file
	logger.Info("Parsing OVA file...")
	ovaPackage, err := ova.ParseOVA(absOVAFile)
	if err != nil {
		return fmt.Errorf("failed to parse OVA file: %w", err)
	}

	logger.WithFields(logrus.Fields{
		"ovf_file":   ovaPackage.OVFFile.Name,
		"vmdk_files": len(ovaPackage.VMDKFiles),
		"total_size": formatBytes(ovaPackage.TotalSize),
	}).Info("OVA file parsed successfully")

	// Add files to tracker
	if ovaPackage.OVFFile != nil {
		tracker.AddFile(ovaPackage.OVFFile.Name, ovaPackage.OVFFile.Size, ovaPackage.OVFFile.SHA1Hash)
	}
	for _, vmdk := range ovaPackage.VMDKFiles {
		tracker.AddFile(vmdk.Name, vmdk.Size, vmdk.SHA1Hash)
	}

	// Create ESXi client
	esxiConfig := esxi.Config{
		Host:     esxiHost,
		Username: username,
		Password: password,
		Insecure: insecure,
	}

	client := esxi.NewClient(esxiConfig)

	// Test connection first
	logger.Info("Testing ESXi connection...")
	if err := client.TestConnection(); err != nil {
		return fmt.Errorf("failed to connect to ESXi: %w", err)
	}

	logger.Info("ESXi connection successful")

	// Connect for real work
	if err := client.Connect(); err != nil {
		return fmt.Errorf("failed to connect to ESXi: %w", err)
	}
	defer client.Disconnect()

	// Get datastore
	ds, err := client.GetDatastore(datastore)
	if err != nil {
		return fmt.Errorf("failed to get datastore: %w", err)
	}

	logger.WithField("datastore", datastore).Info("Datastore found")

	// Create uploader with retry mechanism
	uploader := esxi.NewUploader(client)
	uploader.SetChunkSize(chunkSize)

	// Set progress callback to update tracker
	uploader.SetProgressCallback(func(fileName string, uploaded int64) {
		tracker.UpdateFileProgress(fileName, uploaded)
	})

	// Set file logger for detailed logging
	if fileLogger != nil {
		uploader.SetFileLogger(fileLogger)
	}

	retryManager := retry.NewRetryManager(retry.Config{
		MaxRetries:    maxRetries,
		BaseDelay:     baseDelay,
		MaxDelay:      maxDelay,
		BackoffFactor: 1.5,
		JitterRange:   0.2,
		RetryableErrors: []string{
			"connection refused",
			"timeout",
			"network",
			"temporary failure",
			"503", "502", "504",
			"EOF", "broken pipe",
		},
	})
	retryManager.SetLogger(logger)

	// Start progress monitoring
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				session := tracker.GetSession()
				if !session.IsCompleted {
					fmt.Printf("\r%s Speed: %s/s ETA: %s",
						tracker.PrintProgressBar(50),
						formatBytes(int64(tracker.GetUploadSpeed())),
						tracker.GetETA().Round(time.Second))
				}
			}
		}
	}()

	if verbose {
		fmt.Printf("\n🚀 STARTING UPLOAD PROCESS\n")
		fmt.Printf("═══════════════════════════\n")
		fmt.Printf("📊 Upload Summary:\n")
		fmt.Printf("   - VM Name: %s\n", vmName)
		fmt.Printf("   - Total Files: %d VMDK file(s)\n", len(ovaPackage.VMDKFiles))
		fmt.Printf("   - Total Size: %s\n", formatBytes(ovaPackage.GetTotalVMDKSize()))
		fmt.Printf("   - ESXi Host: %s\n", esxiHost)
		fmt.Printf("   - Datastore: %s\n", datastore)
		fmt.Printf("\n")
	} else if !quiet {
		fmt.Printf("Uploading %s to %s...\n", vmName, esxiHost)
	}

	// Upload each VMDK file
	for i, vmdkFile := range ovaPackage.VMDKFiles {
		if verbose {
			fmt.Printf("📁 PROCESSING FILE %d/%d: %s\n", i+1, len(ovaPackage.VMDKFiles), vmdkFile.Name)
			fmt.Printf("   - Size: %s\n", formatBytes(vmdkFile.Size))
			fmt.Printf("   - Offset in OVA: %d\n", vmdkFile.Offset)
			if vmdkFile.SHA1Hash != "" {
				fmt.Printf("   - SHA1: %s\n", vmdkFile.SHA1Hash)
			}
		}

		fileProgress := tracker.GetFileProgress(vmdkFile.Name)
		if fileProgress != nil && fileProgress.IsCompleted {
			if verbose {
				fmt.Printf("⏭️  File already uploaded, skipping\n\n")
			}
			logger.WithField("file", vmdkFile.Name).Info("File already uploaded, skipping")
			continue
		}

		logger.WithFields(logrus.Fields{
			"file": vmdkFile.Name,
			"size": formatBytes(vmdkFile.Size),
		}).Info("Starting file upload")

		remotePath := fmt.Sprintf("%s/%s", vmName, vmdkFile.Name)
		if verbose {
			fmt.Printf("   - Remote path: %s\n", remotePath)
			fmt.Printf("\n")
		}

		uploadFunc := func() error {
			if useStreaming {
				if workers > 1 {
					if verbose {
						fmt.Printf("🌊 Using PARALLEL STREAMING mode (%d workers, no temp files)\n", workers)
					}
					// Use parallel streaming upload
					return uploader.UploadVMDKFromOVAStreamParallel(absOVAFile, vmdkFile.Offset, vmdkFile.Size, ds, remotePath, vmdkFile.Name, workers, verbose)
				} else {
					if verbose {
						fmt.Printf("🌊 Using STREAMING mode (no temp files)\n")
					}
					// Use single-threaded streaming upload
					return uploader.UploadVMDKFromOVAStreamQuiet(absOVAFile, vmdkFile.Offset, vmdkFile.Size, ds, remotePath, vmdkFile.Name, verbose)
				}
			} else {
				if verbose {
					fmt.Printf("📦 Using EXTRACTION mode (temp files)\n")
				}
				// Use traditional extraction method
				return uploadFileWithProgress(uploader, tracker, absOVAFile, vmdkFile, ds, remotePath, verbose)
			}
		}

		if verbose {
			fmt.Printf("🔄 Starting upload with retry capability...\n")
		}

		err := retryManager.ExecuteWithProgress(ctx, uploadFunc, func(attempt int, lastError error, nextRetry time.Duration) {
			if lastError != nil {
				tracker.IncrementRetryAttempts()
				if verbose {
					fmt.Printf("❌ Upload attempt %d failed: %s\n", attempt, lastError.Error())
					fmt.Printf("⏰ Retrying in %s...\n\n", nextRetry)
				} else if !quiet {
					fmt.Printf("Upload failed (attempt %d), retrying in %s...\n", attempt, nextRetry)
				}
				logger.WithFields(logrus.Fields{
					"file":     vmdkFile.Name,
					"attempt":  attempt,
					"error":    lastError.Error(),
					"retry_in": nextRetry,
				}).Warn("Upload attempt failed, retrying")
			}
		})

		if err != nil {
			if verbose {
				fmt.Printf("💥 FATAL: Upload failed after retries: %s\n", err.Error())
			}
			return fmt.Errorf("failed to upload %s after retries: %w", vmdkFile.Name, err)
		}

		tracker.MarkFileCompleted(vmdkFile.Name)
		if verbose {
			fmt.Printf("✅ FILE UPLOAD COMPLETED: %s\n\n", vmdkFile.Name)
		}
		logger.WithField("file", vmdkFile.Name).Info("File upload completed")
	}

	// Final progress update
	fmt.Printf("\r%s\n", tracker.PrintProgressBar(50))

	session := tracker.GetSession()
	if !quiet {
		fmt.Printf("VMDK upload completed successfully in %s\n", time.Since(session.StartTime).Round(time.Second))
		if session.RetryAttempts > 0 {
			fmt.Printf("Total retry attempts: %d\n", session.RetryAttempts)
		}
	}

	logger.WithFields(logrus.Fields{
		"duration":       time.Since(session.StartTime),
		"total_size":     formatBytes(session.TotalSize),
		"retry_attempts": session.RetryAttempts,
	}).Info("VMDK upload completed successfully")

	// Now create the VM from the OVF descriptor
	if !quiet {
		fmt.Printf("\nCreating VM from OVF descriptor...\n")
	}
	logger.Info("Extracting OVF descriptor and creating VM")

	// Extract OVF content
	ovfContent, err := ovaPackage.ExtractOVFContent()
	if err != nil {
		return fmt.Errorf("failed to extract OVF content: %w", err)
	}

	if verbose {
		fmt.Printf("OVF descriptor extracted (%d bytes)\n", len(ovfContent))
	}

	// Import VM from OVF
	err = client.ImportVMFromOVF(ovfContent, vmName, datastore, network)
	if err != nil {
		return fmt.Errorf("failed to create VM from OVF: %w", err)
	}

	if !quiet {
		fmt.Printf("VM '%s' created successfully!\n", vmName)
	}

	logger.WithField("vm_name", vmName).Info("VM created successfully from OVF")

	// Clean up session file
	tracker.Delete()

	return nil
}

func uploadFileWithProgress(uploader *esxi.Uploader, tracker *progress.Tracker, ovaPath string, vmdkFile *ova.OVAFile, datastore *object.Datastore, remotePath string, verbose bool) error {
	fmt.Printf("🔧 STEP 1: Creating temporary file for VMDK extraction...\n")

	// Create a temporary file for this VMDK
	tmpFile, err := os.CreateTemp("", "vmdk-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	fmt.Printf("✅ Temporary file created: %s\n", tmpFile.Name())
	fmt.Printf("🔧 STEP 2: Opening OVA file for extraction...\n")

	// Extract VMDK from OVA
	ovaFile, err := os.Open(ovaPath)
	if err != nil {
		return fmt.Errorf("failed to open OVA file: %w", err)
	}
	defer ovaFile.Close()

	fmt.Printf("✅ OVA file opened: %s\n", ovaPath)
	fmt.Printf("🔧 STEP 3: Seeking to VMDK offset %d in OVA file...\n", vmdkFile.Offset)

	_, err = ovaFile.Seek(vmdkFile.Offset, 0)
	if err != nil {
		return fmt.Errorf("failed to seek to VMDK offset: %w", err)
	}

	fmt.Printf("✅ Positioned at VMDK offset\n")
	fmt.Printf("🔧 STEP 4: Extracting VMDK data (%s)...\n", formatBytes(vmdkFile.Size))

	// Create a progress reader to track extraction
	extracted := int64(0)
	reader := &progressReader{
		reader: ovaFile,
		onProgress: func(n int) {
			extracted += int64(n)
			if extracted%100000000 == 0 || extracted == vmdkFile.Size { // Log every 100MB or at completion
				fmt.Printf("📦 Extracted: %s / %s (%.1f%%)\n",
					formatBytes(extracted),
					formatBytes(vmdkFile.Size),
					float64(extracted)/float64(vmdkFile.Size)*100)
			}
		},
	}

	written, err := io.CopyN(tmpFile, reader, vmdkFile.Size)
	if err != nil {
		return fmt.Errorf("failed to extract VMDK: %w", err)
	}

	if written != vmdkFile.Size {
		return fmt.Errorf("incomplete VMDK extraction: got %d bytes, expected %d", written, vmdkFile.Size)
	}

	fmt.Printf("✅ VMDK extraction completed: %s\n", formatBytes(written))
	fmt.Printf("🔧 STEP 5: Starting upload to ESXi datastore...\n")
	fmt.Printf("   - Remote path: %s\n", remotePath)
	fmt.Printf("   - Datastore: %s\n", datastore.Name())
	fmt.Printf("   - File size: %s\n", formatBytes(vmdkFile.Size))

	// Reset file position for upload
	_, err = tmpFile.Seek(0, 0)
	if err != nil {
		return fmt.Errorf("failed to reset temp file position: %w", err)
	}

	// Upload the extracted VMDK
	return uploader.UploadVMDKToDatastore(tmpFile.Name(), datastore, remotePath, vmdkFile.Name, vmdkFile.Size, verbose)
}

// progressReader wraps an io.Reader and calls a callback on each read
type progressReader struct {
	reader     io.Reader
	onProgress func(int)
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	if n > 0 && pr.onProgress != nil {
		pr.onProgress(n)
	}
	return n, err
}

func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
