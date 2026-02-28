package player

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// absolutePath 获取绝对路径
func absolutePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	return filepath.Abs(path)
}

// GetWAVDuration 获取 WAV 文件时长（秒）
func GetWAVDuration(filePath string) (float64, error) {
	// 读取 WAV 文件头获取时长
	// WAV 文件头: 44 字节
	// SampleRate: 字节 24-27
	// ByteRate: 字节 28-31
	// DataSize: 字节 40-43

	data := make([]byte, 44)
	f, err := os.Open(filePath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	_, err = f.Read(data)
	if err != nil {
		return 0, err
	}

	// 获取文件大小
	stat, err := f.Stat()
	if err != nil {
		return 0, err
	}

	// ByteRate = SampleRate * NumChannels * BitsPerSample/8
	byteRate := uint32(data[28]) | uint32(data[29])<<8 | uint32(data[30])<<16 | uint32(data[31])<<24

	if byteRate == 0 {
		return 0, fmt.Errorf("无效的 WAV 文件")
	}

	// 数据大小 = 文件大小 - 头大小
	dataSize := stat.Size() - 44

	// 时长 = 数据大小 / 字节率
	duration := float64(dataSize) / float64(byteRate)

	return duration, nil
}

// Play 播放音频文件（阻塞直到播放完成）
func Play(filePath string) error {
	switch runtime.GOOS {
	case "windows":
		return playWindows(filePath)
	case "linux":
		return playLinux(filePath)
	case "darwin":
		return playMac(filePath)
	default:
		return fmt.Errorf("不支持的操作系统: %s", runtime.GOOS)
	}
}

// PlayAsync 异步播放音频，返回停止函数
func PlayAsync(filePath string) (*exec.Cmd, error) {
	switch runtime.GOOS {
	case "windows":
		return playWindowsAsync(filePath)
	case "linux":
		return playLinuxAsync(filePath)
	default:
		return nil, fmt.Errorf("不支持的操作系统: %s", runtime.GOOS)
	}
}

// playWindows 使用 Windows Media Player 播放
func playWindows(filePath string) error {
	// 转换为绝对路径
	absPath, err := absolutePath(filePath)
	if err != nil {
		return fmt.Errorf("获取绝对路径失败: %w", err)
	}

	// 使用 PowerShell 播放音频
	script := fmt.Sprintf(`
$player = New-Object System.Media.SoundPlayer '%s'
$player.PlaySync()
`, absPath)
	cmd := exec.Command("powershell", "-NoProfile", "-Command", script)
	hideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("播放失败: %w, 输出: %s", err, string(output))
	}
	return nil
}

func playWindowsAsync(filePath string) (*exec.Cmd, error) {
	// 转换为绝对路径
	absPath, err := absolutePath(filePath)
	if err != nil {
		return nil, fmt.Errorf("获取绝对路径失败: %w", err)
	}

	// 使用 PowerShell 播放音频
	script := fmt.Sprintf(`
$player = New-Object System.Media.SoundPlayer '%s'
$player.PlaySync()
`, absPath)
	cmd := exec.Command("powershell", "-NoProfile", "-Command", script)
	hideWindow(cmd)
	err = cmd.Start()
	return cmd, err
}

func playLinux(filePath string) error {
	// 尝试使用 aplay
	cmd := exec.Command("aplay", filePath)
	return cmd.Run()
}

func playLinuxAsync(filePath string) (*exec.Cmd, error) {
	cmd := exec.Command("aplay", filePath)
	err := cmd.Start()
	return cmd, err
}

func playMac(filePath string) error {
	cmd := exec.Command("afplay", filePath)
	return cmd.Run()
}

// PlayWithCallback 播放音频，并在指定时间执行回调
func PlayWithCallback(filePath string, callbackTime float64, callback func()) error {
	duration, err := GetWAVDuration(filePath)
	if err != nil {
		return fmt.Errorf("获取音频时长失败: %w", err)
	}

	// 启动异步播放
	cmd, err := PlayAsync(filePath)
	if err != nil {
		return fmt.Errorf("播放失败: %w", err)
	}

	// 如果回调时间有效，设置定时器
	if callbackTime > 0 && callbackTime < duration {
		go func() {
			time.Sleep(time.Duration(callbackTime * float64(time.Second)))
			callback()
		}()
	}

	// 等待播放完成
	return cmd.Wait()
}

// StopAllPlayback 停止所有音频播放进程
// 用于在强制停止播放模式时清理资源
func StopAllPlayback() {
	if runtime.GOOS == "windows" {
		// 停止 PowerShell 音频播放进程
		// 查找并终止正在运行 SoundPlayer 的 PowerShell 进程
		cmd := exec.Command("powershell", "-NoProfile", "-Command",
			"Get-Process powershell -ErrorAction SilentlyContinue | Where-Object {$_.CommandLine -like '*SoundPlayer*'} | Stop-Process -Force -ErrorAction SilentlyContinue")
		hideWindow(cmd)
		cmd.Run()
	} else if runtime.GOOS == "linux" {
		// 停止 aplay 进程
		cmd := exec.Command("pkill", "-f", "aplay")
		cmd.Run()
	}
}
