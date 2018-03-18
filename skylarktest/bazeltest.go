// +build bazel

// Copyright 2018 The Bazel Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Rebinds DataFile, so that it builds data paths as expected by Bazel.

package skylarktest

import (
	"os"
	"path/filepath"
)

func init() {
	testDir := os.Getenv("TEST_SRCDIR")
	DataFile = func(subdir, filename string) string {
		// Late check testDir, to only panic when we actually need it.
		if testDir == "" {
			panic("Environment variable TEST_SRCDIR unset or empty.")
		}
		return filepath.Join(testDir, "com_github_google_skylark", subdir, filename)
	}
}
