package cli

import "golang.org/x/sys/unix"

func isTmpfs(dir string) (bool, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return false, err
	}
	return st.Type == unix.TMPFS_MAGIC, nil
}
