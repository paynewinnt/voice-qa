package adb

import "strings"

// ShellArgs preserves argument boundaries when adb joins arguments for the remote shell.
func ShellArgs(args ...string) []string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
	}
	return []string{"shell", strings.Join(quoted, " ")}
}
