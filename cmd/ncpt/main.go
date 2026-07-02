package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	ncpt "nirvana-city-personnel-terminal"
	"nirvana-city-personnel-terminal/internal/app"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	defaultDataDir := filepath.Join(filepath.Dir(exe), "data")
	dataDir := flag.String("data-dir", defaultDataDir, "SQLite data directory")
	noBrowser := flag.Bool("no-browser", false, "do not open the browser automatically")
	mergePath := flag.String("merge", "", "merge a 7-sheet ncpt xlsx export into the SQLite database and exit")
	flag.Parse()

	if *mergePath != "" {
		return runMerge(*dataDir, *mergePath)
	}

	lock, runningURL, err := app.AcquireInstanceLock(*dataDir)
	if errors.Is(err, app.ErrConflict) {
		if runningURL != "" && !*noBrowser {
			_ = app.OpenBrowser(runningURL)
		}
		fmt.Println("nirvana-city-personnel-terminal is already running:", runningURL)
		return nil
	}
	if err != nil {
		return err
	}
	defer lock.Release()
	defer app.RemoveInstanceURL(*dataDir)

	ctx := context.Background()
	store, err := app.OpenStore(ctx, app.Config{DataDir: *dataDir})
	if err != nil {
		return err
	}
	defer store.Close()

	listener, err := listenLocal(defaultListenPort)
	if err != nil {
		return err
	}
	appURL := "http://" + listener.Addr().String() + "/"
	if err := app.WriteInstanceURL(*dataDir, appURL); err != nil {
		_ = listener.Close()
		return err
	}

	serverApp := app.NewServer(store, ncpt.FrontendFS)
	httpServer := &http.Server{
		Handler:           serverApp.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serverApp.SetShutdown(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
	})

	errCh := make(chan error, 1)
	go func() {
		err := httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()
	if !*noBrowser {
		_ = app.OpenBrowser(appURL)
	}
	fmt.Println("nirvana-city-personnel-terminal:", appURL)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sigCh:
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(ctx)
		return <-errCh
	case err := <-errCh:
		return err
	}
}

const defaultListenPort = 23458

func listenLocal(preferredPort int) (net.Listener, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", preferredPort))
	if err == nil {
		return listener, nil
	}
	if !isAddressInUse(err) {
		return nil, err
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

func isAddressInUse(err error) bool {
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) && errno == syscall.Errno(10048) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "address already in use") || strings.Contains(message, "only one usage")
}

func runMerge(dataDir string, mergePath string) error {
	lock, runningURL, err := app.AcquireInstanceLock(dataDir)
	if errors.Is(err, app.ErrConflict) {
		if runningURL != "" {
			return fmt.Errorf("data-dir is in use by a running app at %s; close the app before running --merge", runningURL)
		}
		return fmt.Errorf("data-dir is in use; close the app before running --merge")
	}
	if err != nil {
		return err
	}
	defer lock.Release()

	ctx := context.Background()
	store, err := app.OpenStore(ctx, app.Config{DataDir: dataDir})
	if err != nil {
		return err
	}
	defer store.Close()

	report, err := store.MergeWorkbook(ctx, mergePath)
	if err != nil {
		return err
	}
	printMergeReport(report)
	return nil
}

func printMergeReport(report *app.MergeReport) {
	fmt.Println("merge completed:", report.Source)
	if report.BackupPath != "" {
		fmt.Println("backup:", report.BackupPath)
	}
	for _, sheet := range app.MergeSheetNames() {
		stats := report.Sheets[sheet]
		fmt.Printf("%s: insert=%d update=%d skip=%d error=%d\n", sheet, stats.Inserted, stats.Updated, stats.Skipped, stats.Errors)
	}
}
