package cli

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/caelis-labs/caelis/internal/filelock"
	"github.com/caelis-labs/caelis/internal/productpaths"
	"github.com/caelis-labs/caelis/internal/servicelifecycle"
)

// This short-lived CLI waiter never serves requests or opens the Store writer.
// It retries the same lifecycle owner after busy work, even if the initiating
// TUI exits. No daemon, protocol, or alternative Host selection is introduced.
func scheduleDeferredHostUpgrade(options productClientOptions, status servicelifecycle.Status) {
	if status.PendingUpgrade == nil || options.LaunchLocalService != nil {
		return // Handler-backed lifecycle tests don't launch real executables.
	}
	err := launchDeferredHostUpgrade(options.StoreDir, status)
	if err != nil {
		// The compatible running Host remains available to the caller.
		recordManagedProductDiagnostic(options.StoreDir, "deferred_upgrade", err)
	}
}

func launchDeferredHostUpgrade(store string, status servicelifecycle.Status) error {
	logDir := productpaths.ServiceLogDir(store)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(logDir, localHostLogFilename), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(status.PendingUpgrade.Executable, "internal-upgrade-host", store, status.InstanceID)
	cmd.Dir = store
	cmd.Env = localHostEnvironment(os.Environ())
	cmd.Stdout, cmd.Stderr = log, log
	configureDetachedLocalHostCommand(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func runDeferredHostUpgrade(ctx context.Context, args []string) error {
	if len(args) != 2 || !filepath.IsAbs(args[0]) || args[1] == "" {
		return errors.New("cli: invalid deferred Host upgrade")
	}
	store, instance := args[0], args[1]
	record, _, err := loadManagedDiscovery(store)
	if errors.Is(err, os.ErrNotExist) || err == nil && record.InstanceID != instance {
		return nil
	}
	if err != nil {
		return err
	}
	options := productClientOptions{StoreDir: store, AppName: record.AppName, UserID: record.PrincipalID}
	manager, candidate, err := newLocalServiceManager(options)
	if err != nil {
		return err
	}
	// One waiter per offered build and original Host. Different newer releases
	// still serialize through Manager's existing Store lifecycle lock.
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(candidate.BuildID+"\x00"+instance)))
	lockCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	lock, err := filelock.Acquire(lockCtx, filepath.Join(store, "runtime/service", "upgrade-"+key+".lock"))
	cancel()
	if errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	if err != nil {
		return err
	}
	defer lock.Close()
	return awaitHostUpgrade(ctx, manager, candidate, instance, time.Second)
}

func awaitHostUpgrade(ctx context.Context, manager servicelifecycle.Manager, candidate servicelifecycle.Candidate, instance string, interval time.Duration) error {
	observe := manager.ObservePhase
	manager.ObservePhase = func(event servicelifecycle.PhaseEvent) {
		if observe != nil && !errors.Is(event.Err, servicelifecycle.ErrHostBusy) {
			observe(event)
		}
	}
	delay := interval
	for {
		status, err := manager.UpgradeInstance(ctx, candidate, instance)
		if errors.Is(err, os.ErrNotExist) || err == nil && status.PendingUpgrade == nil {
			return nil
		}
		if err != nil {
			return err
		}
		// Bounded exponential backoff avoids a uniform full-directory scan on
		// legacy Hosts. Jitter prevents multiple offered builds waking together.
		timer := time.NewTimer(delay - time.Duration(rand.Int64N(max(1, int64(delay/5)))))
		delay = min(5*interval, 2*delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
