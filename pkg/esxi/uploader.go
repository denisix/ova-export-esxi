package esxi

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/vmware/govmomi/object"
)

type UploadProgress struct {
	TotalBytes     int64
	UploadedBytes  int64
	CurrentFile    string
	StartTime      time.Time
	LastUpdate     time.Time
	BytesPerSecond float64
}

type Uploader struct {
	client           *Client
	progress         *UploadProgress
	chunkSize        int64
	progressCallback func(fileName string, uploaded int64)
	fileLogger       *logrus.Logger
}

func NewUploader(client *Client) *Uploader {
	return &Uploader{
		client:    client,
		chunkSize: 32 * 1024 * 1024, // 32MB chunks
		progress: &UploadProgress{
			StartTime: time.Now(),
		},
	}
}

func (u *Uploader) SetChunkSize(size int64) {
	u.chunkSize = size
}

func (u *Uploader) SetProgressCallback(callback func(fileName string, uploaded int64)) {
	u.progressCallback = callback
}

func (u *Uploader) SetFileLogger(logger *logrus.Logger) {
	u.fileLogger = logger
}

func (u *Uploader) GetProgress() *UploadProgress {
	return u.progress
}

// UploadVMDKToDatastore uploads a VMDK file to a datastore using HTTP PUT
func (u *Uploader) UploadVMDKToDatastore(localPath string, datastore *object.Datastore, remotePath, fileName string, size int64, verbose bool) error {
	if verbose {
		fmt.Printf("🌐 UPLOAD STEP 1: Opening local file for upload...\n")
		fmt.Printf("   - Local path: %s\n", localPath)
		fmt.Printf("   - File size: %s\n", formatBytes(size))
	}

	// Open local file
	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open local file: %w", err)
	}
	defer file.Close()

	// Verify file size
	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat local file: %w", err)
	}
	if verbose {
		fmt.Printf("✅ Local file opened, actual size: %s\n", formatBytes(stat.Size()))
		fmt.Printf("🌐 UPLOAD STEP 2: Getting ESXi datastore upload URL...\n")
	}

	// Get upload URL for direct file upload to datastore
	url, err := u.getUploadURL(datastore, remotePath)
	if err != nil {
		return fmt.Errorf("failed to get upload URL: %w", err)
	}

	if verbose {
		fmt.Printf("✅ Upload URL obtained: %s\n", url)
		fmt.Printf("🌐 UPLOAD STEP 3: Starting chunked upload...\n")
		fmt.Printf("   - Chunk size: %s\n", formatBytes(u.chunkSize))
		fmt.Printf("   - Total chunks: %d\n", (size+u.chunkSize-1)/u.chunkSize)
	}

	// Upload the file directly
	return u.uploadFileChunked(file, url, fileName, size, verbose)
}

// UploadVMDKFromOVAStream uploads a VMDK directly from OVA without extraction
func (u *Uploader) UploadVMDKFromOVAStream(ovaPath string, offset, size int64, datastore *object.Datastore, remotePath, fileName string) error {
	return u.UploadVMDKFromOVAStreamQuiet(ovaPath, offset, size, datastore, remotePath, fileName, true)
}

// UploadVMDKFromOVAStreamQuiet uploads with configurable verbosity
func (u *Uploader) UploadVMDKFromOVAStreamQuiet(ovaPath string, offset, size int64, datastore *object.Datastore, remotePath, fileName string, verbose bool) error {
	if verbose {
		fmt.Printf("🌊 STREAM UPLOAD: Direct OVA-to-ESXi streaming\n")
		fmt.Printf("   - OVA file: %s\n", ovaPath)
		fmt.Printf("   - VMDK offset: %s\n", formatBytes(offset))
		fmt.Printf("   - VMDK size: %s\n", formatBytes(size))
		fmt.Printf("   - Remote path: %s\n", remotePath)
	}

	// Get upload URL
	url, err := u.getUploadURL(datastore, remotePath)
	if err != nil {
		return fmt.Errorf("failed to get upload URL: %w", err)
	}

	if verbose {
		fmt.Printf("✅ Upload URL obtained: %s\n", url)
		fmt.Printf("🌊 Starting direct stream upload (no temporary files)...\n")
	}

	// Stream directly from OVA to ESXi
	return u.uploadFromOVAChunkedQuiet(ovaPath, offset, size, url, fileName, verbose)
}

// UploadVMDKFromOVAStreamParallel uploads with parallel workers
func (u *Uploader) UploadVMDKFromOVAStreamParallel(ovaPath string, offset, size int64, datastore *object.Datastore, remotePath, fileName string, workers int, verbose bool) error {
	if verbose {
		fmt.Printf("🌊 PARALLEL STREAM UPLOAD: %d workers\n", workers)
		fmt.Printf("   - OVA file: %s\n", ovaPath)
		fmt.Printf("   - VMDK offset: %s\n", formatBytes(offset))
		fmt.Printf("   - VMDK size: %s\n", formatBytes(size))
		fmt.Printf("   - Remote path: %s\n", remotePath)
	}

	// Get upload URL
	url, err := u.getUploadURL(datastore, remotePath)
	if err != nil {
		return fmt.Errorf("failed to get upload URL: %w", err)
	}

	if verbose {
		fmt.Printf("✅ Upload URL obtained: %s\n", url)
		fmt.Printf("🌊 Starting parallel stream upload (%d workers)...\n", workers)
	}

	// Use parallel upload
	return u.uploadFromOVAParallel(ovaPath, offset, size, url, fileName, workers, verbose)
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

func (u *Uploader) getUploadURL(datastore *object.Datastore, remotePath string) (string, error) {
	// Construct the upload URL manually for ESXi datastore
	// Format: https://hostname/folder/path?dcPath=datacenter&dsName=datastore
	soapClient := u.client.GetSOAPClient()
	if soapClient == nil {
		return "", fmt.Errorf("no SOAP client available")
	}

	baseURL := soapClient.URL()
	uploadURL := fmt.Sprintf("%s://%s/folder/%s?dcPath=ha-datacenter&dsName=%s",
		baseURL.Scheme, baseURL.Host, remotePath, datastore.Name())

	return uploadURL, nil
}

// uploadFromOVAChunked streams data directly from OVA to ESXi in chunks
func (u *Uploader) uploadFromOVAChunked(ovaPath string, offset, totalSize int64, uploadURL, fileName string) error {
	return u.uploadFromOVAChunkedQuiet(ovaPath, offset, totalSize, uploadURL, fileName, true)
}

// uploadFromOVAChunkedQuiet streams data with configurable verbosity
func (u *Uploader) uploadFromOVAChunkedQuiet(ovaPath string, offset, totalSize int64, uploadURL, fileName string, verbose bool) error {
	// Always log to file if available
	if u.fileLogger != nil {
		u.fileLogger.WithFields(logrus.Fields{
			"ova_path":   ovaPath,
			"offset":     offset,
			"total_size": totalSize,
			"upload_url": uploadURL,
			"file_name":  fileName,
			"chunk_size": u.chunkSize,
		}).Info("Starting streaming upload")
	}

	if verbose {
		fmt.Printf("🔗 STREAMING UPLOAD STARTING\n")
		fmt.Printf("   - File: %s\n", fileName)
		fmt.Printf("   - Total size: %s\n", formatBytes(totalSize))
		fmt.Printf("   - Chunk size: %s\n", formatBytes(u.chunkSize))
	}

	u.progress.TotalBytes = totalSize
	u.progress.UploadedBytes = 0
	u.progress.CurrentFile = fileName
	u.progress.StartTime = time.Now()
	u.progress.LastUpdate = time.Now()

	// Create HTTP client with same TLS settings as ESXi client
	if verbose {
		fmt.Printf("🔒 TLS Config: InsecureSkipVerify = %v\n", u.client.insecure)
	}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: u.client.insecure,
		},
	}

	client := &http.Client{
		Timeout:   30 * time.Minute, // 30 minutes per chunk
		Transport: transport,
	}

	var uploadedBytes int64 = 0
	chunkNumber := 1
	totalChunks := (totalSize + u.chunkSize - 1) / u.chunkSize

	if verbose {
		fmt.Printf("📦 Starting stream upload of %d chunks...\n\n", totalChunks)
	}

	for uploadedBytes < totalSize {
		chunkSize := u.chunkSize
		if uploadedBytes+chunkSize > totalSize {
			chunkSize = totalSize - uploadedBytes
		}

		// Only show chunk details in verbose mode
		if verbose {
			fmt.Printf("📤 CHUNK %d/%d: Streaming %s (offset %s)\n",
				chunkNumber, totalChunks,
				formatBytes(chunkSize),
				formatBytes(uploadedBytes))
		}

		err := u.uploadChunkFromOVAQuiet(client, ovaPath, offset+uploadedBytes, chunkSize, uploadURL, totalSize, verbose)
		if err != nil {
			// Always log errors to file
			if u.fileLogger != nil {
				u.fileLogger.WithFields(logrus.Fields{
					"chunk_number": chunkNumber,
					"total_chunks": totalChunks,
					"offset":       uploadedBytes,
					"chunk_size":   chunkSize,
					"error":        err.Error(),
				}).Error("Chunk upload failed")
			}

			if verbose {
				fmt.Printf("❌ CHUNK %d FAILED: %s\n", chunkNumber, err.Error())
			}
			return fmt.Errorf("failed to upload chunk at offset %d: %w", uploadedBytes, err)
		}

		uploadedBytes += chunkSize
		u.progress.UploadedBytes = uploadedBytes
		u.updateProgress()

		// Always log progress to file
		if u.fileLogger != nil {
			percentage := float64(uploadedBytes) / float64(totalSize) * 100
			u.fileLogger.WithFields(logrus.Fields{
				"chunk_number":   chunkNumber,
				"total_chunks":   totalChunks,
				"uploaded_bytes": uploadedBytes,
				"total_bytes":    totalSize,
				"percentage":     percentage,
				"speed_bps":      u.progress.BytesPerSecond,
			}).Debug("Chunk upload completed")
		}

		// Only show chunk completion in verbose mode
		if verbose {
			percentage := float64(uploadedBytes) / float64(totalSize) * 100
			fmt.Printf("✅ CHUNK %d COMPLETED: %.1f%% total progress\n", chunkNumber, percentage)
		}

		// Call progress callback if set (always call, regardless of verbose mode)
		if u.progressCallback != nil {
			u.progressCallback(fileName, uploadedBytes)
			if verbose {
				fmt.Printf("📊 Calling progress callback: %s uploaded\n", formatBytes(uploadedBytes))
			}
		}

		chunkNumber++
		if verbose {
			fmt.Printf("\n")
		}
	}

	if verbose {
		fmt.Printf("🎉 ALL CHUNKS STREAMED SUCCESSFULLY!\n")
	}
	return nil
}

// uploadFromOVAParallel uploads chunks in parallel using multiple workers
func (u *Uploader) uploadFromOVAParallel(ovaPath string, offset, totalSize int64, uploadURL, fileName string, workers int, verbose bool) error {
	// Always log to file if available
	if u.fileLogger != nil {
		u.fileLogger.WithFields(logrus.Fields{
			"ova_path":   ovaPath,
			"offset":     offset,
			"total_size": totalSize,
			"upload_url": uploadURL,
			"file_name":  fileName,
			"chunk_size": u.chunkSize,
			"workers":    workers,
		}).Info("Starting parallel streaming upload")
	}

	if verbose {
		fmt.Printf("🔗 PARALLEL UPLOAD STARTING\n")
		fmt.Printf("   - File: %s\n", fileName)
		fmt.Printf("   - Total size: %s\n", formatBytes(totalSize))
		fmt.Printf("   - Chunk size: %s\n", formatBytes(u.chunkSize))
		fmt.Printf("   - Workers: %d\n", workers)
	}

	u.progress.TotalBytes = totalSize
	u.progress.UploadedBytes = 0
	u.progress.CurrentFile = fileName
	u.progress.StartTime = time.Now()
	u.progress.LastUpdate = time.Now()

	// Create HTTP client with same TLS settings as ESXi client
	if verbose {
		fmt.Printf("🔒 TLS Config: InsecureSkipVerify = %v\n", u.client.insecure)
	}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: u.client.insecure,
		},
	}

	client := &http.Client{
		Timeout:   30 * time.Minute, // 30 minutes per chunk
		Transport: transport,
	}

	totalChunks := (totalSize + u.chunkSize - 1) / u.chunkSize

	if verbose {
		fmt.Printf("📦 Starting parallel upload of %d chunks with %d workers...\n\n", totalChunks, workers)
	}

	// Create work queue and result tracking
	type chunkWork struct {
		chunkNumber int64
		ovaOffset   int64
		chunkSize   int64
	}

	type chunkResult struct {
		chunkNumber int64
		err         error
		size        int64
	}

	workQueue := make(chan chunkWork, totalChunks)
	results := make(chan chunkResult, totalChunks)

	// Progress tracking with mutex
	var progressMutex sync.Mutex
	var completedBytes int64

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			for work := range workQueue {
				if verbose {
					fmt.Printf("🔄 Worker %d: Chunk %d/%d\n", workerID, work.chunkNumber, totalChunks)
				}

				err := u.uploadChunkFromOVAQuiet(client, ovaPath, work.ovaOffset, work.chunkSize, uploadURL, totalSize, verbose)

				results <- chunkResult{
					chunkNumber: work.chunkNumber,
					err:         err,
					size:        work.chunkSize,
				}

				if err == nil {
					// Update progress safely
					progressMutex.Lock()
					completedBytes += work.chunkSize
					u.progress.UploadedBytes = completedBytes
					u.updateProgress()

					// Call progress callback if set
					if u.progressCallback != nil {
						u.progressCallback(fileName, completedBytes)
					}
					progressMutex.Unlock()

					if verbose {
						percentage := float64(completedBytes) / float64(totalSize) * 100
						fmt.Printf("✅ Worker %d: Chunk %d completed (%.1f%%)\n", workerID, work.chunkNumber, percentage)
					}
				} else {
					if verbose {
						fmt.Printf("❌ Worker %d: Chunk %d failed: %s\n", workerID, work.chunkNumber, err.Error())
					}
				}
			}
		}(i)
	}

	// Queue all chunks
	var currentOffset int64 = 0
	for chunkNum := int64(1); chunkNum <= totalChunks; chunkNum++ {
		chunkSize := u.chunkSize
		if currentOffset+chunkSize > totalSize {
			chunkSize = totalSize - currentOffset
		}

		workQueue <- chunkWork{
			chunkNumber: chunkNum,
			ovaOffset:   offset + currentOffset,
			chunkSize:   chunkSize,
		}

		currentOffset += chunkSize
	}
	close(workQueue)

	// Wait for all workers to complete
	wg.Wait()
	close(results)

	// Collect results and check for errors
	var errors []error
	successCount := 0

	for result := range results {
		if result.err != nil {
			errors = append(errors, fmt.Errorf("chunk %d failed: %w", result.chunkNumber, result.err))
		} else {
			successCount++
		}
	}

	if len(errors) > 0 {
		if verbose {
			fmt.Printf("❌ %d chunks failed out of %d total\n", len(errors), totalChunks)
		}
		// Return the first error (could be enhanced to return all)
		return errors[0]
	}

	if verbose {
		fmt.Printf("🎉 ALL %d CHUNKS UPLOADED SUCCESSFULLY WITH %d WORKERS!\n", successCount, workers)
	}

	// Log completion to file
	if u.fileLogger != nil {
		u.fileLogger.WithFields(logrus.Fields{
			"file_name":       fileName,
			"total_chunks":    totalChunks,
			"workers":         workers,
			"total_size":      totalSize,
			"upload_duration": time.Since(u.progress.StartTime),
		}).Info("Parallel upload completed successfully")
	}

	return nil
}

// uploadChunkFromOVA uploads a single chunk directly from OVA file
func (u *Uploader) uploadChunkFromOVA(client *http.Client, ovaPath string, ovaOffset, chunkSize int64, uploadURL string, totalSize int64) error {
	return u.uploadChunkFromOVAQuiet(client, ovaPath, ovaOffset, chunkSize, uploadURL, totalSize, true)
}

// uploadChunkFromOVAQuiet uploads a chunk with configurable verbosity
func (u *Uploader) uploadChunkFromOVAQuiet(client *http.Client, ovaPath string, ovaOffset, chunkSize int64, uploadURL string, totalSize int64, verbose bool) error {
	// Always log to file if available
	if u.fileLogger != nil {
		u.fileLogger.WithFields(logrus.Fields{
			"ova_offset": ovaOffset,
			"chunk_size": chunkSize,
			"upload_url": uploadURL,
		}).Debug("Starting chunk upload from OVA")
	}

	// Only show detailed chunk operations in verbose mode
	if verbose {
		fmt.Printf("🌊 Opening OVA for chunk read at offset %s\n", formatBytes(ovaOffset))
	}

	// Open OVA file for this chunk
	ovaFile, err := os.Open(ovaPath)
	if err != nil {
		return fmt.Errorf("failed to open OVA file: %w", err)
	}
	defer ovaFile.Close()

	// Seek to the specific position in the OVA
	_, err = ovaFile.Seek(ovaOffset, io.SeekStart)
	if err != nil {
		return fmt.Errorf("failed to seek to offset %d: %w", ovaOffset, err)
	}

	// Create a limited reader for the chunk
	chunkReader := io.LimitReader(ovaFile, chunkSize)

	// Only show HTTP request creation in verbose mode
	if verbose {
		fmt.Printf("🌊 Creating HTTP request for chunk upload\n")
	}

	// Create the HTTP request
	req, err := http.NewRequest("PUT", uploadURL, chunkReader)
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Set headers for chunked upload
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Length", fmt.Sprintf("%d", chunkSize))

	// Add authentication (basic auth from the client)
	if u.client.username != "" && u.client.password != "" {
		req.SetBasicAuth(u.client.username, u.client.password)
	}

	// Only show HTTP request sending in verbose mode
	if verbose {
		fmt.Printf("🌊 Sending HTTP request to ESXi\n")
	}

	// Execute the request
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	// Always log response to file
	if u.fileLogger != nil {
		u.fileLogger.WithFields(logrus.Fields{
			"status_code": resp.StatusCode,
			"status":      resp.Status,
			"chunk_size":  chunkSize,
		}).Debug("HTTP response received")
	}

	// Only show HTTP response in verbose mode
	if verbose {
		fmt.Printf("🌊 Response status: %d %s\n", resp.StatusCode, resp.Status)
	}

	// Check response status
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated &&
		resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)

		// Log error response to file
		if u.fileLogger != nil {
			u.fileLogger.WithFields(logrus.Fields{
				"status_code":   resp.StatusCode,
				"status":        resp.Status,
				"response_body": string(body),
			}).Error("HTTP upload failed")
		}

		return fmt.Errorf("upload failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Only show success message in verbose mode
	if verbose {
		fmt.Printf("🌊 Chunk uploaded successfully\n")
	}
	return nil
}

func (u *Uploader) uploadFileChunked(file *os.File, uploadURL, fileName string, totalSize int64, verbose bool) error {
	if verbose {
		fmt.Printf("🔗 CHUNKED UPLOAD STARTING\n")
		fmt.Printf("   - File: %s\n", fileName)
		fmt.Printf("   - Total size: %s\n", formatBytes(totalSize))
		fmt.Printf("   - Chunk size: %s\n", formatBytes(u.chunkSize))
	}

	u.progress.TotalBytes = totalSize
	u.progress.UploadedBytes = 0
	u.progress.CurrentFile = fileName
	u.progress.StartTime = time.Now()
	u.progress.LastUpdate = time.Now()

	// Create HTTP client with same TLS settings as ESXi client
	if verbose {
		fmt.Printf("🔒 TLS Config: InsecureSkipVerify = %v\n", u.client.insecure)
	}
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: u.client.insecure,
		},
	}

	client := &http.Client{
		Timeout:   30 * time.Minute, // 30 minutes per chunk
		Transport: transport,
	}

	var offset int64 = 0
	chunkNumber := 1
	totalChunks := (totalSize + u.chunkSize - 1) / u.chunkSize

	if verbose {
		fmt.Printf("📦 Starting upload of %d chunks...\n\n", totalChunks)
	}

	for offset < totalSize {
		chunkSize := u.chunkSize
		if offset+chunkSize > totalSize {
			chunkSize = totalSize - offset
		}

		if verbose {
			fmt.Printf("📤 CHUNK %d/%d: Uploading %s (offset %s)\n",
				chunkNumber, totalChunks,
				formatBytes(chunkSize),
				formatBytes(offset))
		}

		err := u.uploadChunk(client, file, uploadURL, offset, chunkSize, totalSize)
		if err != nil {
			if verbose {
				fmt.Printf("❌ CHUNK %d FAILED: %s\n", chunkNumber, err.Error())
			}
			return fmt.Errorf("failed to upload chunk at offset %d: %w", offset, err)
		}

		offset += chunkSize
		u.progress.UploadedBytes = offset
		u.updateProgress()

		if verbose {
			percentage := float64(offset) / float64(totalSize) * 100
			fmt.Printf("✅ CHUNK %d COMPLETED: %.1f%% total progress\n", chunkNumber, percentage)
		}

		// Call progress callback if set
		if u.progressCallback != nil {
			if verbose {
				fmt.Printf("📊 Calling progress callback: %s uploaded\n", formatBytes(offset))
			}
			u.progressCallback(fileName, offset)
		}

		chunkNumber++
		if verbose {
			fmt.Printf("\n")
		}
	}

	if verbose {
		fmt.Printf("🎉 ALL CHUNKS UPLOADED SUCCESSFULLY!\n")
	}
	return nil
}

func (u *Uploader) uploadChunk(client *http.Client, file *os.File, uploadURL string, offset, chunkSize, totalSize int64) error {
	// Debug logging
	fmt.Printf("DEBUG: Uploading chunk offset=%d, size=%d, total=%d\n", offset, chunkSize, totalSize)
	fmt.Printf("DEBUG: Upload URL: %s\n", uploadURL)

	// Seek to the offset
	_, err := file.Seek(offset, io.SeekStart)
	if err != nil {
		return fmt.Errorf("failed to seek to offset %d: %w", offset, err)
	}

	// Create a limited reader for the chunk
	chunkReader := io.LimitReader(file, chunkSize)

	// Create the HTTP request
	req, err := http.NewRequest("PUT", uploadURL, chunkReader)
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}

	// Set headers for chunked upload
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Length", fmt.Sprintf("%d", chunkSize))

	// Add authentication (basic auth from the client)
	if u.client.username != "" && u.client.password != "" {
		req.SetBasicAuth(u.client.username, u.client.password)
	}

	// Debug request headers
	fmt.Printf("DEBUG: Request headers: %+v\n", req.Header)

	// Execute the request
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	// Debug response
	fmt.Printf("DEBUG: Response status: %d %s\n", resp.StatusCode, resp.Status)

	// Check response status
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated &&
		resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed with status %d: %s", resp.StatusCode, string(body))
	}

	fmt.Printf("DEBUG: Chunk uploaded successfully\n")
	return nil
}

func (u *Uploader) updateProgress() {
	now := time.Now()
	elapsed := now.Sub(u.progress.StartTime).Seconds()

	if elapsed > 0 {
		u.progress.BytesPerSecond = float64(u.progress.UploadedBytes) / elapsed
	}

	u.progress.LastUpdate = now
}

// GetUploadSpeed returns the current upload speed in bytes per second
func (u *Uploader) GetUploadSpeed() float64 {
	return u.progress.BytesPerSecond
}

// GetETA returns estimated time to completion
func (u *Uploader) GetETA() time.Duration {
	if u.progress.BytesPerSecond <= 0 {
		return 0
	}

	remainingBytes := u.progress.TotalBytes - u.progress.UploadedBytes
	eta := time.Duration(float64(remainingBytes) / u.progress.BytesPerSecond * float64(time.Second))

	return eta
}

// GetProgressPercentage returns the upload progress as a percentage
func (u *Uploader) GetProgressPercentage() float64 {
	if u.progress.TotalBytes == 0 {
		return 0
	}

	return float64(u.progress.UploadedBytes) / float64(u.progress.TotalBytes) * 100
}
