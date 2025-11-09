package retry

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/sirupsen/logrus"
)

type RetryManager struct {
	maxRetries      int
	baseDelay       time.Duration
	maxDelay        time.Duration
	backoffFactor   float64
	jitterRange     float64
	logger          *logrus.Logger
	retryableErrors []string
}

type Config struct {
	MaxRetries      int           // 0 for infinite retries
	BaseDelay       time.Duration // Initial delay between retries
	MaxDelay        time.Duration // Maximum delay between retries
	BackoffFactor   float64       // Multiplier for exponential backoff
	JitterRange     float64       // Random jitter factor (0.0 to 1.0)
	RetryableErrors []string      // List of error strings that should trigger a retry
}

type RetryableFunc func() error

type RetryStats struct {
	Attempts     int
	TotalTime    time.Duration
	LastError    error
	LastAttempt  time.Time
	NextAttempt  time.Time
	CurrentDelay time.Duration
}

func NewRetryManager(config Config) *RetryManager {
	if config.MaxRetries == 0 {
		config.MaxRetries = -1 // -1 indicates infinite retries
	}
	if config.BaseDelay == 0 {
		config.BaseDelay = 1 * time.Second
	}
	if config.MaxDelay == 0 {
		config.MaxDelay = 5 * time.Minute
	}
	if config.BackoffFactor == 0 {
		config.BackoffFactor = 2.0
	}
	if config.JitterRange == 0 {
		config.JitterRange = 0.1
	}

	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel)

	return &RetryManager{
		maxRetries:      config.MaxRetries,
		baseDelay:       config.BaseDelay,
		maxDelay:        config.MaxDelay,
		backoffFactor:   config.BackoffFactor,
		jitterRange:     config.JitterRange,
		retryableErrors: config.RetryableErrors,
		logger:          logger,
	}
}

func (rm *RetryManager) SetLogger(logger *logrus.Logger) {
	rm.logger = logger
}

// Execute runs the given function with retry logic
func (rm *RetryManager) Execute(ctx context.Context, fn RetryableFunc) error {
	return rm.ExecuteWithStats(ctx, fn, nil)
}

// ExecuteWithStats runs the function and provides retry statistics via callback
func (rm *RetryManager) ExecuteWithStats(ctx context.Context, fn RetryableFunc, statsCallback func(*RetryStats)) error {
	stats := &RetryStats{
		LastAttempt: time.Now(),
	}

	startTime := time.Now()
	attempt := 0

	for {
		attempt++
		stats.Attempts = attempt
		stats.LastAttempt = time.Now()

		// Update callback if provided
		if statsCallback != nil {
			statsCallback(stats)
		}

		rm.logger.WithFields(logrus.Fields{
			"attempt": attempt,
			"elapsed": time.Since(startTime),
		}).Info("Attempting operation")

		// Execute the function
		err := fn()
		if err == nil {
			rm.logger.WithFields(logrus.Fields{
				"attempts": attempt,
				"duration": time.Since(startTime),
			}).Info("Operation succeeded")
			return nil
		}

		stats.LastError = err
		stats.TotalTime = time.Since(startTime)

		// Check if we should retry
		if !rm.shouldRetry(err, attempt) {
			rm.logger.WithFields(logrus.Fields{
				"attempts": attempt,
				"error":    err.Error(),
				"duration": time.Since(startTime),
			}).Error("Operation failed, no more retries")
			return fmt.Errorf("operation failed after %d attempts: %w", attempt, err)
		}

		// Calculate delay for next attempt
		delay := rm.calculateDelay(attempt)
		stats.CurrentDelay = delay
		stats.NextAttempt = time.Now().Add(delay)

		rm.logger.WithFields(logrus.Fields{
			"attempt":   attempt,
			"error":     err.Error(),
			"nextRetry": delay,
		}).Warn("Operation failed, retrying")

		// Check context cancellation before sleeping
		select {
		case <-ctx.Done():
			return fmt.Errorf("operation cancelled: %w", ctx.Err())
		case <-time.After(delay):
			// Continue to next attempt
		}
	}
}

func (rm *RetryManager) shouldRetry(err error, attempt int) bool {
	// Check if we've exceeded maximum attempts
	if rm.maxRetries > 0 && attempt >= rm.maxRetries {
		return false
	}

	// If no specific retryable errors are defined, retry all errors
	if len(rm.retryableErrors) == 0 {
		return true
	}

	// Check if the error matches any retryable error patterns
	errStr := err.Error()
	for _, retryableErr := range rm.retryableErrors {
		if containsString(errStr, retryableErr) {
			return true
		}
	}

	return false
}

func (rm *RetryManager) calculateDelay(attempt int) time.Duration {
	// Calculate exponential backoff
	delay := time.Duration(float64(rm.baseDelay) * math.Pow(rm.backoffFactor, float64(attempt-1)))

	// Apply maximum delay limit
	if delay > rm.maxDelay {
		delay = rm.maxDelay
	}

	// Add jitter to prevent thundering herd
	if rm.jitterRange > 0 {
		jitter := time.Duration(float64(delay) * rm.jitterRange * (rand.Float64()*2 - 1))
		delay += jitter
	}

	// Ensure delay is not negative
	if delay < 0 {
		delay = rm.baseDelay
	}

	return delay
}

// ExecuteWithProgress executes a function with retry and progress reporting
func (rm *RetryManager) ExecuteWithProgress(ctx context.Context, fn RetryableFunc, progressCallback func(attempt int, lastError error, nextRetry time.Duration)) error {
	return rm.ExecuteWithStats(ctx, fn, func(stats *RetryStats) {
		if progressCallback != nil {
			progressCallback(stats.Attempts, stats.LastError, stats.CurrentDelay)
		}
	})
}

// IsRetryableError checks if an error should trigger a retry
func (rm *RetryManager) IsRetryableError(err error) bool {
	if len(rm.retryableErrors) == 0 {
		return true
	}

	errStr := err.Error()
	for _, retryableErr := range rm.retryableErrors {
		if containsString(errStr, retryableErr) {
			return true
		}
	}

	return false
}

// GetConfig returns the current retry configuration
func (rm *RetryManager) GetConfig() Config {
	return Config{
		MaxRetries:      rm.maxRetries,
		BaseDelay:       rm.baseDelay,
		MaxDelay:        rm.maxDelay,
		BackoffFactor:   rm.backoffFactor,
		JitterRange:     rm.jitterRange,
		RetryableErrors: rm.retryableErrors,
	}
}

// containsString checks if a string contains a substring (case-insensitive)
func containsString(str, substr string) bool {
	return len(str) >= len(substr) &&
		(substr == "" ||
			str == substr ||
			containsSubstring(str, substr))
}

func containsSubstring(str, substr string) bool {
	for i := 0; i <= len(str)-len(substr); i++ {
		if str[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// CreateInfiniteRetryManager creates a retry manager with infinite retries
func CreateInfiniteRetryManager() *RetryManager {
	return NewRetryManager(Config{
		MaxRetries:    0, // Infinite
		BaseDelay:     2 * time.Second,
		MaxDelay:      2 * time.Minute,
		BackoffFactor: 1.5,
		JitterRange:   0.2,
	})
}

// CreateNetworkRetryManager creates a retry manager optimized for network operations
func CreateNetworkRetryManager() *RetryManager {
	return NewRetryManager(Config{
		MaxRetries:    0, // Infinite
		BaseDelay:     1 * time.Second,
		MaxDelay:      30 * time.Second,
		BackoffFactor: 2.0,
		JitterRange:   0.3,
		RetryableErrors: []string{
			"connection refused",
			"timeout",
			"network",
			"temporary failure",
			"503",
			"502",
			"504",
			"EOF",
			"broken pipe",
		},
	})
}
