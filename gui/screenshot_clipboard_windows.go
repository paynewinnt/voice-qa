//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	screenshotUser32       = windows.NewLazySystemDLL("user32.dll")
	screenshotKernel32     = windows.NewLazySystemDLL("kernel32.dll")
	clipboardCreateWindow  = screenshotUser32.NewProc("CreateWindowExW")
	clipboardDestroyWindow = screenshotUser32.NewProc("DestroyWindow")
	clipboardOpen          = screenshotUser32.NewProc("OpenClipboard")
	clipboardClose         = screenshotUser32.NewProc("CloseClipboard")
	clipboardEmpty         = screenshotUser32.NewProc("EmptyClipboard")
	clipboardSetData       = screenshotUser32.NewProc("SetClipboardData")
	clipboardGlobalAlloc   = screenshotKernel32.NewProc("GlobalAlloc")
	clipboardGlobalFree    = screenshotKernel32.NewProc("GlobalFree")
	clipboardGlobalLock    = screenshotKernel32.NewProc("GlobalLock")
	clipboardGlobalUnlock  = screenshotKernel32.NewProc("GlobalUnlock")
	clipboardMoveMemory    = screenshotKernel32.NewProc("RtlMoveMemory")
	clipboardSetLastError  = screenshotKernel32.NewProc("SetLastError")
)

func copyScreenshotToClipboard(ctx context.Context, path string) (resultErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dib, err := screenshotClipboardDIB(data)
	if err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// A real owner HWND is required by EmptyClipboard/SetClipboardData. No visible window is created.
	className := [...]uint16{'S', 'T', 'A', 'T', 'I', 'C', 0}
	hwnd, _, err := clipboardCreateWindow.Call(0, uintptr(unsafe.Pointer(&className[0])), 0, 0, 0, 0, 0, 0, ^uintptr(2), 0, 0, 0)
	if hwnd == 0 {
		return fmt.Errorf("创建剪贴板窗口失败: %w", err)
	}
	defer func() {
		if ok, _, err := clipboardDestroyWindow.Call(hwnd); ok == 0 {
			resultErr = errors.Join(resultErr, fmt.Errorf("释放剪贴板窗口失败: %w", err))
		}
	}()

	handle, _, err := clipboardGlobalAlloc.Call(0x0042, uintptr(len(dib))) // GMEM_MOVEABLE | GMEM_ZEROINIT
	if handle == 0 {
		return fmt.Errorf("分配剪贴板内存失败: %w", err)
	}
	defer func() {
		if handle != 0 {
			if remaining, _, err := clipboardGlobalFree.Call(handle); remaining != 0 {
				resultErr = errors.Join(resultErr, fmt.Errorf("释放剪贴板内存失败: %w", err))
			}
		}
	}()
	address, _, err := clipboardGlobalLock.Call(handle)
	if address == 0 {
		return fmt.Errorf("锁定剪贴板内存失败: %w", err)
	}
	clipboardMoveMemory.Call(address, uintptr(unsafe.Pointer(&dib[0])), uintptr(len(dib)))
	runtime.KeepAlive(dib)
	clipboardSetLastError.Call(0)
	if locked, _, err := clipboardGlobalUnlock.Call(handle); locked == 0 && err != windows.ERROR_SUCCESS {
		return fmt.Errorf("解锁剪贴板内存失败: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for {
		if err := waitCtx.Err(); err != nil {
			return fmt.Errorf("剪贴板忙，请稍后重试: %w", err)
		}
		if opened, _, _ := clipboardOpen.Call(hwnd); opened != 0 {
			break
		}
		select {
		case <-waitCtx.Done():
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer func() {
		if closed, _, err := clipboardClose.Call(); closed == 0 {
			resultErr = errors.Join(resultErr, fmt.Errorf("关闭剪贴板失败: %w", err))
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if emptied, _, err := clipboardEmpty.Call(); emptied == 0 {
		return fmt.Errorf("准备剪贴板失败: %w", err)
	}
	if copied, _, err := clipboardSetData.Call(8, handle); copied == 0 { // CF_DIB, image data rather than a file path.
		return fmt.Errorf("写入剪贴板图片失败: %w", err)
	}
	handle = 0 // Windows owns the allocated memory after SetClipboardData succeeds.
	return nil
}
