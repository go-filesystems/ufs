// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"errors"
	iofs "io/fs"
	"testing"
)

// ⛔ The error contract from go-filesystems/interface: a path that is not
// there must satisfy errors.Is(err, fs.ErrNotExist).
//
// This driver already had ONE sentinel for it, so the whole contract is met by
// wrapping that -- every existing `return ErrNotFound` now answers, without a
// single call site changing. Drivers that build the error at each site needed
// the sites read one by one instead.
//
// ⚠ ErrNotFound stays exported and stays the same variable, so a caller
// already doing errors.Is(err, ufs.ErrNotFound) is unaffected.
//
// Nothing about internal structure was wrapped. This package distinguishes
// them already -- a corrupt record is ErrCorrupt, not ErrNotFound -- and a
// server that got 404 for a corrupt image would hide a real fault behind a
// routine one.
func TestErrNotFoundSatisfiesErrNotExist(t *testing.T) {
	if !errors.Is(ErrNotFound, iofs.ErrNotExist) {
		t.Errorf("errors.Is(ErrNotFound, fs.ErrNotExist) is false for %q", ErrNotFound)
	}
	// Still its own sentinel, for callers that name it.
	if !errors.Is(ErrNotFound, ErrNotFound) {
		t.Error("ErrNotFound stopped matching itself")
	}
	// And wrapping it keeps both, which is how a driver reports context.
	wrapped := errors.Join(ErrNotFound)
	if !errors.Is(wrapped, iofs.ErrNotExist) {
		t.Error("a wrapped ErrNotFound lost fs.ErrNotExist")
	}
}
