package lsp

import "syscall"

// kill0 probes whether pid is alive (signal 0). Used by spawn_test to assert
// Close reaped the child.
func kill0(pid int) error {
	return syscall.Kill(pid, 0)
}
