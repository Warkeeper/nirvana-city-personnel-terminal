package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nirvana-city-personnel-terminal/internal/app"
)

func TestRunMergeRejectsRunningInstance(t *testing.T) {
	dataDir := t.TempDir()
	lock, _, err := app.AcquireInstanceLock(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := app.WriteInstanceURL(dataDir, server.URL); err != nil {
		t.Fatal(err)
	}
	defer app.RemoveInstanceURL(dataDir)

	err = runMerge(dataDir, "unused.xlsx")
	if err == nil {
		t.Fatal("expected running instance conflict")
	}
	if !strings.Contains(err.Error(), "data-dir is in use") {
		t.Fatalf("unexpected error: %v", err)
	}
}
