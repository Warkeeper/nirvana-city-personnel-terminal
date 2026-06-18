package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type InstanceLock struct {
	path string
	file *os.File
}

func AcquireInstanceLock(dataDir string) (*InstanceLock, string, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, "", err
	}
	lockPath := filepath.Join(dataDir, "ncfms.lock")
	urlPath := filepath.Join(dataDir, "ncfms.url")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err == nil {
		_, _ = file.WriteString(fmt.Sprintf("%d\n", os.Getpid()))
		return &InstanceLock{path: lockPath, file: file}, "", nil
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, "", err
	}
	runningURL := readURLFile(urlPath)
	if runningURL != "" && healthOK(runningURL) {
		return nil, runningURL, ErrConflict
	}
	_ = os.Remove(lockPath)
	file, err = os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return nil, "", err
	}
	_, _ = file.WriteString(fmt.Sprintf("%d\n", os.Getpid()))
	return &InstanceLock{path: lockPath, file: file}, "", nil
}

func (l *InstanceLock) Release() error {
	if l == nil {
		return nil
	}
	if l.file != nil {
		_ = l.file.Close()
	}
	return os.Remove(l.path)
}

func WriteInstanceURL(dataDir, appURL string) error {
	return os.WriteFile(filepath.Join(dataDir, "ncfms.url"), []byte(appURL+"\n"), 0644)
}

func RemoveInstanceURL(dataDir string) {
	_ = os.Remove(filepath.Join(dataDir, "ncfms.url"))
}

func readURLFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func healthOK(appURL string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(appURL, "/")+"/api/v1/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func OpenBrowser(appURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", appURL)
	case "darwin":
		cmd = exec.Command("open", appURL)
	default:
		cmd = exec.Command("xdg-open", appURL)
	}
	return cmd.Start()
}
