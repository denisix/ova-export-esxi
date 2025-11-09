# OVA ESXi Uploader

A robust, reliable OVA uploader for ESXi servers with infinite retry capability and resume functionality. This tool recreates the functionality of VMware's `ovftool` but with enhanced reliability for unstable network connections.

## Features

- **Infinite Retry**: Automatically retries failed uploads with exponential backoff
- **Resumable Uploads**: Resume interrupted uploads from where they left off
- **Chunked Transfers**: Split large files into manageable chunks for reliable transfer
- **Progress Tracking**: Real-time progress monitoring with ETA calculations
- **Session Persistence**: Save upload sessions to survive application restarts
- **Checksum Validation**: Verify data integrity using SHA1 hashes from manifest files
- **Detailed Logging**: Comprehensive logging for debugging network issues

## How it Works

### OVA File Structure
OVA files are TAR archives containing:
- **OVF descriptor** (.ovf) - VM configuration metadata
- **VMDK files** (.vmdk) - Virtual disk images
- **Manifest file** (.mf) - SHA1 checksums for validation
- **Certificate file** (.cert) - Optional digital signatures

### Upload Process
1. **Parse OVA**: Extract file metadata and validate structure
2. **Connect to ESXi**: Authenticate using vSphere APIs
3. **Extract VMDKs**: Extract disk images from TAR archive to temporary files
4. **Chunked Upload**: Upload files in 32MB chunks with resume capability
5. **Retry Logic**: Infinite retries with exponential backoff on failures
6. **Progress Tracking**: Real-time monitoring with session persistence

### Differences from ovftool
- **Retry Mechanism**: ovftool fails on network errors; this tool retries infinitely
- **Resume Capability**: Continue interrupted uploads from last successful chunk
- **Better Progress**: Real-time progress bars with speed and ETA
- **Session Management**: Persist upload state across application restarts

## Installation

```bash
# Clone the repository
git clone <repository-url>
cd ova-export-esxi

# Build the application
go build -o ova-esxi-uploader .

# Or install directly
go install .
```

## Usage

### Basic Upload
```bash
ova-esxi-uploader upload vm.ova esxi.example.com --datastore datastore1
```

### Advanced Options
```bash
ova-esxi-uploader upload vm.ova esxi.example.com \
  --datastore datastore1 \
  --vm-name "My Virtual Machine" \
  --network "VM Network" \
  --username root \
  --password mypassword \
  --chunk-size 67108864 \
  --insecure
```

### Resume Previous Upload
```bash
# List available sessions
ova-esxi-uploader list-sessions

# Resume most recent session
ova-esxi-uploader resume

# Resume specific session
ova-esxi-uploader resume --session-id 1699123456
```

### Session Management
```bash
# List all upload sessions
ova-esxi-uploader list-sessions

# Clean up old session files
ova-esxi-uploader clean-sessions
```

## Command Line Options

### Upload Command
- `--username, -u`: ESXi username (default: root)
- `--password, -p`: ESXi password (prompts if not provided)
- `--datastore, -d`: Target datastore name (required)
- `--vm-name, -n`: Virtual machine name (defaults to OVA filename)
- `--network`: Network name for VM (default: "VM Network")
- `--insecure`: Skip SSL certificate verification (default: true)
- `--chunk-size`: Upload chunk size in bytes (default: 32MB)
- `--max-retries`: Maximum retry attempts (0 for infinite)
- `--base-delay`: Base delay between retries (default: 2s)
- `--max-delay`: Maximum delay between retries (default: 2m)
- `--resume`: Resume from previous upload session
- `--session-id`: Specific session ID to resume

### Global Options
- `--verbose, -v`: Enable verbose logging
- `--quiet, -q`: Suppress all output except errors

## Configuration

### Retry Configuration
The tool uses intelligent retry logic with:
- **Exponential Backoff**: Delays increase exponentially (1.5x multiplier)
- **Jitter**: Random variation to prevent thundering herd
- **Maximum Delay**: Caps retry delays at 2 minutes
- **Retryable Errors**: Only retries network-related errors

### Network Error Patterns
The following error patterns trigger automatic retry:
- Connection refused
- Network timeouts
- HTTP 502, 503, 504 errors
- EOF and broken pipe errors
- Temporary DNS failures

## Architecture

```
ova-esxi-uploader/
├── cmd/                    # CLI commands
│   ├── root.go            # Root command setup
│   ├── upload.go          # Upload command implementation
│   └── sessions.go        # Session management commands
├── pkg/
│   ├── ova/               # OVA file parsing
│   │   └── parser.go      # TAR archive extraction and validation
│   ├── esxi/              # ESXi client and uploader
│   │   ├── client.go      # vSphere API client
│   │   └── uploader.go    # Chunked upload implementation
│   ├── retry/             # Retry management
│   │   └── manager.go     # Exponential backoff with jitter
│   └── progress/          # Progress tracking
│       └── tracker.go     # Session persistence and monitoring
└── main.go                # Application entry point
```

## Examples

### Upload with Custom Settings
```bash
ova-esxi-uploader upload large-vm.ova esxi01.company.com \
  --datastore "SSD_Datastore" \
  --vm-name "Production-DB-Server" \
  --network "Production-VLAN" \
  --chunk-size 134217728 \
  --username administrator \
  --verbose
```

### Resume Interrupted Upload
If your upload is interrupted by network issues:
```bash
# Check available sessions
ova-esxi-uploader list-sessions

# Resume the upload
ova-esxi-uploader resume --session-id 1699123456
```

## Troubleshooting

### Common Issues

1. **Connection Refused**
   - Verify ESXi host is reachable
   - Check if SSH/HTTPS is enabled on ESXi
   - Confirm firewall settings

2. **Authentication Failed**
   - Verify username and password
   - Check if account has necessary privileges
   - Ensure account is not locked

3. **Datastore Not Found**
   - List available datastores using vSphere client
   - Verify datastore name spelling
   - Check datastore accessibility

4. **Insufficient Space**
   - Verify datastore has enough free space
   - Check OVA file size requirements
   - Consider using thin provisioning

### Logging
Enable verbose logging for detailed troubleshooting:
```bash
ova-esxi-uploader upload vm.ova esxi.example.com --datastore ds1 --verbose
```

## Dependencies

- [govmomi](https://github.com/vmware/govmomi): VMware vSphere API client
- [cobra](https://github.com/spf13/cobra): CLI framework
- [logrus](https://github.com/sirupsen/logrus): Structured logging

## License

This project is open source. Please check the license file for details.