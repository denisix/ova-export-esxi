package ova

import (
	"archive/tar"
	"crypto/sha1"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type OVAPackage struct {
	FilePath     string
	OVFFile      *OVAFile
	VMDKFiles    []*OVAFile
	ManifestFile *OVAFile
	CertFile     *OVAFile
	TotalSize    int64
}

type OVAFile struct {
	Name     string
	Size     int64
	Offset   int64
	SHA1Hash string
}

type ManifestEntry struct {
	FileName string
	SHA1Hash string
}

func ParseOVA(ovaPath string) (*OVAPackage, error) {
	file, err := os.Open(ovaPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open OVA file: %w", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat OVA file: %w", err)
	}

	pkg := &OVAPackage{
		FilePath:  ovaPath,
		TotalSize: stat.Size(),
		VMDKFiles: make([]*OVAFile, 0),
	}

	tarReader := tar.NewReader(file)
	var offset int64 = 0

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read tar archive: %w", err)
		}

		if header.Typeflag != tar.TypeReg {
			offset += header.Size
			continue
		}

		ovaFile := &OVAFile{
			Name:   header.Name,
			Size:   header.Size,
			Offset: offset,
		}

		ext := strings.ToLower(filepath.Ext(header.Name))
		switch ext {
		case ".ovf":
			pkg.OVFFile = ovaFile
		case ".vmdk":
			pkg.VMDKFiles = append(pkg.VMDKFiles, ovaFile)
		case ".mf":
			pkg.ManifestFile = ovaFile
		case ".cert":
			pkg.CertFile = ovaFile
		}

		offset += header.Size
	}

	if pkg.OVFFile == nil {
		return nil, fmt.Errorf("no OVF file found in OVA package")
	}

	if len(pkg.VMDKFiles) == 0 {
		return nil, fmt.Errorf("no VMDK files found in OVA package")
	}

	// Parse manifest file if present
	if pkg.ManifestFile != nil {
		manifest, err := parseManifestFile(ovaPath, pkg.ManifestFile)
		if err != nil {
			return nil, fmt.Errorf("failed to parse manifest: %w", err)
		}

		// Update SHA1 hashes from manifest
		updateHashesFromManifest(pkg, manifest)
	}

	return pkg, nil
}

func parseManifestFile(ovaPath string, manifestFile *OVAFile) ([]ManifestEntry, error) {
	file, err := os.Open(ovaPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	_, err = file.Seek(manifestFile.Offset, io.SeekStart)
	if err != nil {
		return nil, err
	}

	content := make([]byte, manifestFile.Size)
	_, err = io.ReadFull(file, content)
	if err != nil {
		return nil, err
	}

	var entries []ManifestEntry
	lines := strings.Split(string(content), "\n")

	// Pattern matches both formats: "SHA1(file.ext)= hash" and "SHA1 (file.ext) = hash"
	re := regexp.MustCompile(`SHA1\s*\(([^)]+)\)\s*=\s*([a-fA-F0-9]+)`)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		matches := re.FindStringSubmatch(line)
		if len(matches) == 3 {
			entries = append(entries, ManifestEntry{
				FileName: matches[1],
				SHA1Hash: strings.ToLower(matches[2]),
			})
		}
	}

	return entries, nil
}

func updateHashesFromManifest(pkg *OVAPackage, manifest []ManifestEntry) {
	manifestMap := make(map[string]string)
	for _, entry := range manifest {
		manifestMap[entry.FileName] = entry.SHA1Hash
	}

	if pkg.OVFFile != nil {
		if hash, ok := manifestMap[pkg.OVFFile.Name]; ok {
			pkg.OVFFile.SHA1Hash = hash
		}
	}

	for _, vmdk := range pkg.VMDKFiles {
		if hash, ok := manifestMap[vmdk.Name]; ok {
			vmdk.SHA1Hash = hash
		}
	}
}

func ValidateFileChecksum(ovaPath string, ovaFile *OVAFile) error {
	if ovaFile.SHA1Hash == "" {
		return nil // No hash to validate
	}

	file, err := os.Open(ovaPath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.Seek(ovaFile.Offset, io.SeekStart)
	if err != nil {
		return err
	}

	hash := sha1.New()
	_, err = io.CopyN(hash, file, ovaFile.Size)
	if err != nil {
		return err
	}

	calculatedHash := fmt.Sprintf("%x", hash.Sum(nil))
	if strings.ToLower(calculatedHash) != strings.ToLower(ovaFile.SHA1Hash) {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s",
			ovaFile.Name, ovaFile.SHA1Hash, calculatedHash)
	}

	return nil
}

func (pkg *OVAPackage) GetTotalVMDKSize() int64 {
	var total int64
	for _, vmdk := range pkg.VMDKFiles {
		total += vmdk.Size
	}
	return total
}

func (pkg *OVAPackage) ListFiles() []string {
	var files []string
	if pkg.OVFFile != nil {
		files = append(files, pkg.OVFFile.Name)
	}
	for _, vmdk := range pkg.VMDKFiles {
		files = append(files, vmdk.Name)
	}
	if pkg.ManifestFile != nil {
		files = append(files, pkg.ManifestFile.Name)
	}
	if pkg.CertFile != nil {
		files = append(files, pkg.CertFile.Name)
	}
	return files
}
