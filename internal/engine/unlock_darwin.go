package engine

import (
	"io/fs"
	"path/filepath"
	"syscall"
)

// User-settable flags that make rsync's rename/unlink fail with EPERM:
// Finder's "Locked" checkbox (uchg; on exFAT, the DOS read-only attribute)
// and append-only.
const lockFlags = 0x2 | 0x4 // UF_IMMUTABLE | UF_APPEND

// unlockTree clears lock flags under the given scope of root so rsync can
// replace or delete those files. Only ever called on the replica. It returns
// how many entries it unlocked and the ones it could not (e.g. system flags).
func unlockTree(root string, paths []string, skipDir string) (n int, failed []string) {
	scopes := []string{root}
	if len(paths) > 0 {
		scopes = scopes[:0]
		for _, p := range paths {
			scopes = append(scopes, filepath.Join(root, p))
		}
	}
	for _, s := range scopes {
		filepath.WalkDir(s, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && p == filepath.Join(root, skipDir) {
				return filepath.SkipDir
			}
			if d.Type()&fs.ModeSymlink != 0 {
				return nil // chflags would follow the link
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok || st.Flags&lockFlags == 0 {
				return nil
			}
			if err := syscall.Chflags(p, int(st.Flags&^lockFlags)); err != nil {
				rel, _ := filepath.Rel(root, p)
				failed = append(failed, rel)
				return nil
			}
			n++
			return nil
		})
	}
	return n, failed
}
