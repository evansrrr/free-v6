package mihomo

import (
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const mihomoDownloadURL = "https://api.gitproxy.dev/github.com/MetaCubeX/mihomo/releases/download/Prerelease-Alpha/mihomo-windows-amd64-v3-alpha-5019cc0.zip"
const maxRuntimeDownload = 150 << 20

type DownloadResult struct {
	Version string
	Path    string
	SHA256  string
}

func DownloadLatest(ctx context.Context, root string) (DownloadResult, error) {
	client := &http.Client{}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, mihomoDownloadURL, nil)
	if err != nil {
		return DownloadResult{}, err
	}
	request.Header.Set("User-Agent", "freev6")
	response, err := client.Do(request)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("download mihomo runtime: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return DownloadResult{}, fmt.Errorf("mihomo runtime download returned HTTP %s", response.Status)
	}
	temporary, err := os.CreateTemp("", "freev6-mihomo-*.zip")
	if err != nil {
		return DownloadResult{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hash := sha256.New()
	limited := io.LimitReader(io.TeeReader(response.Body, hash), maxRuntimeDownload)
	if _, err := io.Copy(temporary, limited); err != nil {
		temporary.Close()
		return DownloadResult{}, fmt.Errorf("save mihomo runtime download: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return DownloadResult{}, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	runtimeDir := filepath.Join(root, "runtime")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return DownloadResult{}, err
	}
	outputPath, err := extractRuntime(temporaryPath, runtimeDir)
	if err != nil {
		return DownloadResult{}, err
	}
	return DownloadResult{Version: "Prerelease-Alpha", Path: outputPath, SHA256: digest}, nil
}

func extractRuntime(archivePath, runtimeDir string) (string, error) {
	outputPath := filepath.Join(runtimeDir, "mihomo-windows-amd64-v3.exe")
	name := strings.ToLower(filepath.Base(archivePath))

	if strings.HasSuffix(name, ".zip") {
		archive, err := zip.OpenReader(archivePath)
		if err != nil {
			return "", fmt.Errorf("open mihomo archive: %w", err)
		}
		defer archive.Close()
		for _, file := range archive.File {
			lower := strings.ToLower(file.Name)
			if strings.HasSuffix(lower, ".exe") {
				input, err := file.Open()
				if err != nil {
					return "", err
				}
				output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
				if err == nil {
					_, err = io.Copy(output, io.LimitReader(input, maxRuntimeDownload))
				}
				_ = input.Close()
				_ = output.Close()
				if err != nil {
					return "", fmt.Errorf("extract mihomo runtime: %w", err)
				}
				return outputPath, nil
			}
		}
		return "", fmt.Errorf("mihomo zip contains no executable")
	}

	input, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer input.Close()
	reader, err := gzip.NewReader(input)
	if err != nil {
		return "", fmt.Errorf("open mihomo gzip: %w", err)
	}
	defer reader.Close()
	output, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(output, io.LimitReader(reader, maxRuntimeDownload))
	closeErr := output.Close()
	if copyErr != nil {
		return "", fmt.Errorf("extract mihomo gzip: %w", copyErr)
	}
	if closeErr != nil {
		return "", closeErr
	}
	return outputPath, nil
}
