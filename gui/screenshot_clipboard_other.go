//go:build !windows

package main

import (
	"context"
	"fmt"
)

func copyScreenshotToClipboard(ctx context.Context, path string) error {
	return fmt.Errorf("图片剪贴板目前仅支持 Windows")
}
