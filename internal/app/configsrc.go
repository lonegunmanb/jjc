package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	getter "github.com/hashicorp/go-getter/v2"

	"github.com/lonegunmanb/jjc/internal/app/sysevent"
)

// ResolveConfigSrc materialises ConfigSrc as a local directory the rest of
// the gateway can read router.hcl + playbook .md files from. The returned
// cleanup MUST be invoked at process shutdown.
//
// Resolution rules:
//
//  1. If src points at an existing local directory, it is used in place and
//     cleanup is a no-op.
//  2. Otherwise hashicorp/go-getter v2 is asked to download src into a
//     per-process temp directory (jjc-config-*). cleanup removes that temp
//     directory.
//
// The function does NOT validate router.hcl / playbook contents — callers
// downstream (router.LoadConfig, prompttmpl.New) surface those errors with
// their own structured events.
func ResolveConfigSrc(ctx context.Context, src string, logger sysevent.Sink) (dir string, cleanup func(), err error) {
	noopCleanup := func() {}
	if src == "" {
		return "", noopCleanup, errors.New("config src is empty")
	}

	if info, statErr := os.Stat(src); statErr == nil {
		if !info.IsDir() {
			return "", noopCleanup, fmt.Errorf("config src %q exists locally but is not a directory; pass a directory or a remote go-getter URL", src)
		}
		abs, absErr := filepath.Abs(src)
		if absErr != nil {
			abs = src
		}
		sysevent.Emitf(logger, "config_src_resolved_local", "src=%s dir=%s", src, abs)
		return abs, noopCleanup, nil
	} else if !os.IsNotExist(statErr) {
		// stat failed for a reason other than "not exists" (e.g.
		// permission denied on a local path). Surface that — go-getter
		// would also fail and the underlying os error is more useful.
		return "", noopCleanup, fmt.Errorf("inspect config src %q: %w", src, statErr)
	}

	tmpDir, err := os.MkdirTemp("", "jjc-config-*")
	if err != nil {
		return "", noopCleanup, fmt.Errorf("create config src temp dir: %w", err)
	}
	cleanup = func() {
		if rmErr := os.RemoveAll(tmpDir); rmErr != nil {
			sysevent.Emitf(logger, "config_src_tempdir_cleanup_failed", "dir=%s err=%v", tmpDir, rmErr)
			return
		}
		sysevent.Emitf(logger, "config_src_tempdir_cleaned", "dir=%s", tmpDir)
	}

	sysevent.Emitf(logger, "config_src_download_start", "src=%s dst=%s", src, tmpDir)
	req := &getter.Request{
		Src:     src,
		Dst:     tmpDir,
		GetMode: getter.ModeDir,
	}
	client := &getter.Client{
		Getters:       getter.Getters,
		Decompressors: getter.Decompressors,
	}
	if _, gerr := client.Get(ctx, req); gerr != nil {
		cleanup()
		return "", noopCleanup, fmt.Errorf("download config src %q: %w", src, gerr)
	}
	sysevent.Emitf(logger, "config_src_download_done", "src=%s dst=%s", src, tmpDir)
	return tmpDir, cleanup, nil
}
