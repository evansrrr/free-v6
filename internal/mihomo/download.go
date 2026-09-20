package mihomo

import (
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const latestReleaseURL = "https://api.github.com/repos/MetaCubeX/mihomo/releases/latest"
const maxRuntimeDownload = 150 << 20

type releaseAsset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Digest string `json:"digest"`
}

type releaseInfo struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

type DownloadResult struct {
	Version string
	Path    string
	SHA256  string
}

func SelectWindowsAMD64Asset(release releaseInfo) (releaseAsset, error) {
	for _, asset := range release.Assets {
		name := strings.ToLower(asset.Name)
		if strings.Contains(name, "windows-amd64") && strings.Contains(name, "alpha") && (strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".gz")) {
			return asset, nil
		}
	}
	return releaseAsset{}, fmt.Errorf("no Windows amd64 mihomo Alpha asset in release %s", release.TagName)
}

func DownloadLatest(ctx context.Context, root string) (DownloadResult, error) {
	client := &http.Client{}
	releaseRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		return DownloadResult{}, err
	}
	releaseRequest.Header.Set("Accept", "application/vnd.github+json")
	releaseRequest.Header.Set("User-Agent", "freev6")
	releaseResponse, err := client.Do(releaseRequest)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("request mihomo release metadata: %w", err)
	}
	defer releaseResponse.Body.Close()
	if releaseResponse.StatusCode != http.StatusOK {
		return DownloadResult{}, fmt.Errorf("mihomo release metadata returned HTTP %s", releaseResponse.Status)
	}
	var release releaseInfo
	if err := json.NewDecoder(releaseResponse.Body).Decode(&release); err != nil {
		return DownloadResult{}, fmt.Errorf("decode mihomo release metadata: %w", err)
	}
	asset, err := SelectWindowsAMD64Asset(release)
	if err != nil {
		return DownloadResult{}, err
	}
	downloadRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return DownloadResult{}, err
	}
	downloadRequest.Header.Set("User-Agent", "freev6")
	downloadResponse, err := client.Do(downloadRequest)
	if err != nil {
		return DownloadResult{}, fmt.Errorf("download mihomo runtime: %w", err)
	}
	defer downloadResponse.Body.Close()
	if downloadResponse.StatusCode != http.StatusOK {
		return DownloadResult{}, fmt.Errorf("mihomo runtime download returned HTTP %s", downloadResponse.Status)
	}
	temporary, err := os.CreateTemp("", "freev6-mihomo-*")
	if err != nil {
		return DownloadResult{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	hash := sha256.New()
	limited := io.LimitReader(io.TeeReader(downloadResponse.Body, hash), maxRuntimeDownload)
	if _, err := io.Copy(temporary, limited); err != nil {
		temporary.Close()
		return DownloadResult{}, fmt.Errorf("save mihomo runtime download: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return DownloadResult{}, err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if expected := strings.TrimPrefix(asset.Digest, "sha256:"); expected != "" && !strings.EqualFold(expected, digest) {
		return DownloadResult{}, fmt.Errorf("mihomo runtime checksum mismatch: got %s want %s", digest, expected)
	}
	runtimeDir := filepath.Join(root, "runtime")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return DownloadResult{}, err
	}
	outputPath, err := extractRuntime(temporaryPath, asset.Name, runtimeDir)
	if err != nil {
		return DownloadResult{}, err
	}
	return DownloadResult{Version: release.TagName, Path: outputPath, SHA256: digest}, nil
}

func extractRuntime(archivePath, archiveName, runtimeDir string) (string, error) {
	if strings.HasSuffix(strings.ToLower(archiveName), ".zip") {
		archive, err := zip.OpenReader(archivePath)
		if err != nil {
			return "", fmt.Errorf("open mihomo archive: %w", err)
		}
		defer archive.Close()
		for _, file := range archive.File {
			if strings.HasSuffix(strings.ToLower(file.Name), ".exe") {
				outputPath := filepath.Join(runtimeDir, "mihomo-windows-amd64-v3.exe")
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
	outputPath := filepath.Join(runtimeDir, "mihomo-windows-amd64-v3.exe")
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
