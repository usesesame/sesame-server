//go:build unix

package ops

import (
	"io/fs"
	"os"
	"syscall"
)

func ownLike(reference, root string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	info, err := os.Stat(reference)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return ownTree(root, int(stat.Uid), int(stat.Gid))
}

func ownTree(root string, uid, gid int) error {
	rooted, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer rooted.Close()
	return fs.WalkDir(rooted.FS(), ".", func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return rooted.Lchown(path, uid, gid)
	})
}
