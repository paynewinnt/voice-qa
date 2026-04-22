package adb

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

func init() {
	adbPath = resolveLocalAdbPath()
}

func resolveLocalAdbPath() string {
	adbName := "adb"
	if runtime.GOOS == "windows" {
		adbName = "adb.exe"
	}

	if exePath, err := os.Executable(); err == nil {
		localAdb := filepath.Join(filepath.Dir(exePath), "adb", adbName)
		if _, err := os.Stat(localAdb); err == nil {
			return localAdb
		}
	}

	if cwd, err := os.Getwd(); err == nil {
		localAdb := filepath.Join(cwd, "adb", adbName)
		if _, err := os.Stat(localAdb); err == nil {
			return localAdb
		}
	}

	return filepath.Join("adb", adbName)
}

// ParseArgs 将命令字符串拆分为 adb 参数，支持单引号、双引号和转义。
func ParseArgs(command string) ([]string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, nil
	}

	var (
		args    []string
		current strings.Builder
		quote   rune
		escaped bool
	)

	flush := func() {
		if current.Len() == 0 {
			return
		}
		args = append(args, current.String())
		current.Reset()
	}

	for _, r := range command {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}

		switch {
		case r == '\\':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
		case unicode.IsSpace(r):
			flush()
		default:
			current.WriteRune(r)
		}
	}

	if escaped {
		return nil, fmt.Errorf("命令结尾存在未完成的转义符")
	}
	if quote != 0 {
		return nil, fmt.Errorf("命令存在未闭合的引号")
	}

	flush()
	return args, nil
}
