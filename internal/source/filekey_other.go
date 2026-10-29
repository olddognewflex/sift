//go:build !unix

package source

import "os"

// fileKey reports no identity; callers fall back to comparing files.
func fileKey(os.FileInfo) (any, bool) { return nil, false }
