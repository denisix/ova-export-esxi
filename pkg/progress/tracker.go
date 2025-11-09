package progress

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

type FileProgress struct {
	FileName       string    `json:"fileName"`
	TotalSize      int64     `json:"totalSize"`
	UploadedSize   int64     `json:"uploadedSize"`
	ChunksTotal    int       `json:"chunksTotal"`
	ChunksUploaded int       `json:"chunksUploaded"`
	StartTime      time.Time `json:"startTime"`
	LastUpdate     time.Time `json:"lastUpdate"`
	IsCompleted    bool      `json:"isCompleted"`
	SHA1Hash       string    `json:"sha1Hash,omitempty"`
}

type UploadSession struct {
	SessionID     string                   `json:"sessionId"`
	OVAFile       string                   `json:"ovaFile"`
	ESXiHost      string                   `json:"esxiHost"`
	Datastore     string                   `json:"datastore"`
	VMName        string                   `json:"vmName"`
	TotalSize     int64                    `json:"totalSize"`
	UploadedSize  int64                    `json:"uploadedSize"`
	StartTime     time.Time                `json:"startTime"`
	LastUpdate    time.Time                `json:"lastUpdate"`
	IsCompleted   bool                     `json:"isCompleted"`
	Files         map[string]*FileProgress `json:"files"`
	RetryAttempts int                      `json:"retryAttempts"`
}

type Tracker struct {
	session      *UploadSession
	sessionFile  string
	logger       *logrus.Logger
	mutex        sync.RWMutex
	autoSave     bool
	saveInterval time.Duration
	stopSaving   chan bool
}

func NewTracker(sessionID, ovaFile, esxiHost, datastore, vmName string) *Tracker {
	session := &UploadSession{
		SessionID:  sessionID,
		OVAFile:    ovaFile,
		ESXiHost:   esxiHost,
		Datastore:  datastore,
		VMName:     vmName,
		StartTime:  time.Now(),
		LastUpdate: time.Now(),
		Files:      make(map[string]*FileProgress),
	}

	sessionFile := fmt.Sprintf(".upload-session-%s.json", sessionID)

	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel)

	tracker := &Tracker{
		session:      session,
		sessionFile:  sessionFile,
		logger:       logger,
		autoSave:     true,
		saveInterval: 5 * time.Second,
		stopSaving:   make(chan bool),
	}

	// Start auto-save goroutine
	go tracker.autoSaveLoop()

	return tracker
}

func LoadTracker(sessionFile string) (*Tracker, error) {
	data, err := os.ReadFile(sessionFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	var session UploadSession
	err = json.Unmarshal(data, &session)
	if err != nil {
		return nil, fmt.Errorf("failed to parse session file: %w", err)
	}

	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel)

	tracker := &Tracker{
		session:      &session,
		sessionFile:  sessionFile,
		logger:       logger,
		autoSave:     true,
		saveInterval: 5 * time.Second,
		stopSaving:   make(chan bool),
	}

	// Start auto-save goroutine
	go tracker.autoSaveLoop()

	return tracker, nil
}

func (t *Tracker) SetLogger(logger *logrus.Logger) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	t.logger = logger
}

func (t *Tracker) AddFile(fileName string, totalSize int64, sha1Hash string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	chunkSize := int64(32 * 1024 * 1024) // 32MB chunks
	chunksTotal := int((totalSize + chunkSize - 1) / chunkSize)

	t.session.Files[fileName] = &FileProgress{
		FileName:       fileName,
		TotalSize:      totalSize,
		UploadedSize:   0,
		ChunksTotal:    chunksTotal,
		ChunksUploaded: 0,
		StartTime:      time.Now(),
		LastUpdate:     time.Now(),
		IsCompleted:    false,
		SHA1Hash:       sha1Hash,
	}

	t.session.TotalSize += totalSize
	t.session.LastUpdate = time.Now()
}

func (t *Tracker) UpdateFileProgress(fileName string, uploadedSize int64) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	if file, exists := t.session.Files[fileName]; exists {
		oldUploaded := file.UploadedSize
		file.UploadedSize = uploadedSize
		file.LastUpdate = time.Now()

		chunkSize := int64(32 * 1024 * 1024)
		file.ChunksUploaded = int(uploadedSize / chunkSize)
		if uploadedSize%chunkSize > 0 {
			file.ChunksUploaded++
		}

		// Update total session progress
		t.session.UploadedSize += (uploadedSize - oldUploaded)
		t.session.LastUpdate = time.Now()

		if uploadedSize >= file.TotalSize {
			file.IsCompleted = true
		}
	}
}

func (t *Tracker) MarkFileCompleted(fileName string) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	if file, exists := t.session.Files[fileName]; exists {
		if !file.IsCompleted {
			// Update session progress if file wasn't already marked complete
			remaining := file.TotalSize - file.UploadedSize
			t.session.UploadedSize += remaining
			file.UploadedSize = file.TotalSize
		}
		file.IsCompleted = true
		file.ChunksUploaded = file.ChunksTotal
		file.LastUpdate = time.Now()
		t.session.LastUpdate = time.Now()
	}

	// Check if all files are completed
	allCompleted := true
	for _, file := range t.session.Files {
		if !file.IsCompleted {
			allCompleted = false
			break
		}
	}

	if allCompleted {
		t.session.IsCompleted = true
	}
}

func (t *Tracker) IncrementRetryAttempts() {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	t.session.RetryAttempts++
	t.session.LastUpdate = time.Now()
}

func (t *Tracker) GetSession() *UploadSession {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	// Create a deep copy to avoid race conditions
	sessionCopy := *t.session
	sessionCopy.Files = make(map[string]*FileProgress)
	for k, v := range t.session.Files {
		fileCopy := *v
		sessionCopy.Files[k] = &fileCopy
	}

	return &sessionCopy
}

func (t *Tracker) GetFileProgress(fileName string) *FileProgress {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	if file, exists := t.session.Files[fileName]; exists {
		fileCopy := *file
		return &fileCopy
	}
	return nil
}

func (t *Tracker) GetOverallProgress() (float64, int64, int64) {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	if t.session.TotalSize == 0 {
		return 0, 0, 0
	}

	percentage := float64(t.session.UploadedSize) / float64(t.session.TotalSize) * 100
	return percentage, t.session.UploadedSize, t.session.TotalSize
}

func (t *Tracker) GetUploadSpeed() float64 {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	elapsed := time.Since(t.session.StartTime).Seconds()
	if elapsed <= 0 {
		return 0
	}

	return float64(t.session.UploadedSize) / elapsed
}

func (t *Tracker) GetETA() time.Duration {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	speed := t.GetUploadSpeed()
	if speed <= 0 {
		return 0
	}

	remainingBytes := t.session.TotalSize - t.session.UploadedSize
	eta := time.Duration(float64(remainingBytes) / speed * float64(time.Second))

	return eta
}

func (t *Tracker) Save() error {
	t.mutex.RLock()
	defer t.mutex.RUnlock()

	data, err := json.MarshalIndent(t.session, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}

	err = os.WriteFile(t.sessionFile, data, 0644)
	if err != nil {
		return fmt.Errorf("failed to write session file: %w", err)
	}

	return nil
}

func (t *Tracker) autoSaveLoop() {
	ticker := time.NewTicker(t.saveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if t.autoSave {
				if err := t.Save(); err != nil {
					t.logger.WithError(err).Error("Failed to auto-save session")
				}
			}
		case <-t.stopSaving:
			return
		}
	}
}

func (t *Tracker) EnableAutoSave(enable bool) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	t.autoSave = enable
}

func (t *Tracker) SetSaveInterval(interval time.Duration) {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	t.saveInterval = interval
}

func (t *Tracker) Close() {
	close(t.stopSaving)
	t.Save() // Final save
}

func (t *Tracker) Delete() error {
	t.Close()
	return os.Remove(t.sessionFile)
}

func (t *Tracker) GetSessionFile() string {
	return t.sessionFile
}

// FindExistingSessions looks for existing upload session files
func FindExistingSessions(directory string) ([]string, error) {
	if directory == "" {
		directory = "."
	}

	pattern := filepath.Join(directory, ".upload-session-*.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to search for session files: %w", err)
	}

	return matches, nil
}

// PrintProgressBar creates a visual progress bar
func (t *Tracker) PrintProgressBar(width int) string {
	percentage, uploaded, total := t.GetOverallProgress()

	if width <= 0 {
		width = 50
	}

	filled := int(percentage * float64(width) / 100)
	if filled > width {
		filled = width
	}

	bar := ""
	for i := 0; i < filled; i++ {
		bar += "█"
	}
	for i := filled; i < width; i++ {
		bar += "░"
	}

	return fmt.Sprintf("[%s] %.1f%% (%s/%s)",
		bar, percentage,
		formatBytes(uploaded),
		formatBytes(total))
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
