//go:build !darwin

package engine

// unlockTree is a no-op where BSD file flags do not exist.
func unlockTree(root string, paths []string, skipDir string) (int, []string) { return 0, nil }
