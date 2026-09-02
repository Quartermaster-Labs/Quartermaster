//go:build windows

package process

// ExeLibEnv is a no-op on Windows: the loader's default search order already
// includes the executable's own directory, so self-contained bundles find
// their DLLs without help.
func ExeLibEnv(env []string, exe string) []string {
	return env
}
