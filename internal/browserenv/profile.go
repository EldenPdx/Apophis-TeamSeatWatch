package browserenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const DefaultRoot = "/var/lib/teamseatwatch/browser-profiles"
const processMarker = "TSW_BROWSER_PROFILE_ID="

var binaryCheck sync.Once
var binaryError error

// VerifyExecutable fails closed on an unqualified build. The deployed path has
// no system-browser fallback; the immutable image owns the executable.
func VerifyExecutable() error {
	binaryCheck.Do(func() {
		file, err := os.Open(Executable)
		if err != nil {
			binaryError = errors.New("qualified browser executable unavailable")
			return
		}
		defer file.Close()
		h := sha256.New()
		if _, err = io.Copy(h, file); err != nil || hex.EncodeToString(h.Sum(nil)) != ExecutableSHA256 {
			binaryError = errors.New("qualified browser executable checksum mismatch")
		}
	})
	return binaryError
}

type Profile struct {
	Directory  string
	RecipePath string
	marker     string
	lock       *os.File
}

// Open serializes directory mutation across service processes. Idle profiles
// retain files only; waiting for this lock launches no browser.
func Open(ctx context.Context, root string, e Environment) (*Profile, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	if root == "" {
		root = DefaultRoot
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = privateDirectory(root); err != nil {
		return nil, err
	}
	directory := filepath.Join(root, e.ProfileID.String())
	if err = privateDirectory(directory); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(directory, ".environment.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			lock.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			lock.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	p := &Profile{Directory: directory, RecipePath: filepath.Join(directory, "recipe.json"), marker: processMarker + e.ProfileID.String(), lock: lock}
	// A crashed service may have left Chromium children in the same namespace.
	// The inherited marker scopes recovery to this exact profile, regardless of
	// PID reuse or the old container hostname. Keep the lock until all exit.
	p.CleanupProcesses()
	if err = p.removeCaches(); err != nil {
		p.Close()
		return nil, errors.New("browser profile cache recovery failed")
	}
	for _, name := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie"} {
		if err = os.Remove(filepath.Join(directory, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			p.Close()
			return nil, err
		}
	}
	if err = os.WriteFile(p.RecipePath, e.Recipe, 0600); err != nil {
		p.Close()
		return nil, err
	}
	if err = os.Chmod(p.RecipePath, 0600); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

func privateDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("browser profile directory invalid")
	}
	return os.Chmod(path, 0700)
}

func (p *Profile) ProcessEnvironment() string { return p.marker }

// removeCaches runs only behind the profile lock and process-exit barrier.
// Website storage and browser identity stay intact; unknown paths are retained.
func (p *Profile) removeCaches() error {
	root, err := os.OpenRoot(p.Directory)
	if err != nil {
		return err
	}
	defer root.Close()
	if info, err := root.Lstat("Default"); err == nil && !info.IsDir() {
		return errors.New("browser profile Default directory invalid")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var result error
	for _, name := range []string{
		"Default/Cache", "Default/Code Cache", "GPUPersistentCache",
		"Default/GPUCache", "Default/DawnWebGPUCache", "Default/DawnGraphiteCache",
	} {
		result = errors.Join(result, root.RemoveAll(name))
	}
	return result
}

// CleanupProcesses is also the final barrier before unlocking and returning the
// proxy lease. Marker-bearing children are killed individually and by process
// group; unrelated processes and other profiles are never selected.
func (p *Profile) CleanupProcesses() {
	for {
		entries, _ := os.ReadDir("/proc")
		alive := false
		for _, entry := range entries {
			pid, err := strconv.Atoi(entry.Name())
			if err != nil {
				continue
			}
			env, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "environ"))
			if err != nil || !strings.Contains("\x00"+string(env), "\x00"+p.marker+"\x00") {
				continue
			}
			alive = true
			if group, err := syscall.Getpgid(pid); err == nil && group == pid {
				_ = syscall.Kill(-group, syscall.SIGKILL)
			}
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		if !alive {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (p *Profile) Close() {
	if p == nil || p.lock == nil {
		return
	}
	p.CleanupProcesses()
	if err := p.removeCaches(); err != nil {
		// Cleanup failure must remain visible without exposing profile paths or
		// turning an otherwise completed login into a credential failure.
		slog.Warn("browser_profile_cache_cleanup_failed", "code", "profile_cache_cleanup_failed")
	}
	for _, name := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie"} {
		_ = os.Remove(filepath.Join(p.Directory, name))
	}
	_ = syscall.Flock(int(p.lock.Fd()), syscall.LOCK_UN)
	_ = p.lock.Close()
	p.lock = nil
}
