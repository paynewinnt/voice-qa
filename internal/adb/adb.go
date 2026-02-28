package adb

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ffmpegPath 缓存 ffmpeg 可执行文件路径
var ffmpegPath string

// findFFmpeg 查找 ffmpeg 可执行文件
func findFFmpeg() string {
	if ffmpegPath != "" {
		return ffmpegPath
	}

	ffmpegName := "ffmpeg"
	if runtime.GOOS == "windows" {
		ffmpegName = "ffmpeg.exe"
	}

	// 1. 程序同目录下的 ffmpeg 子目录
	if exePath, err := os.Executable(); err == nil {
		localFFmpeg := filepath.Join(filepath.Dir(exePath), "ffmpeg", ffmpegName)
		if _, err := os.Stat(localFFmpeg); err == nil {
			ffmpegPath = localFFmpeg
			return ffmpegPath
		}
	}

	// 2. 当前工作目录下的 ffmpeg 子目录
	if cwd, err := os.Getwd(); err == nil {
		localFFmpeg := filepath.Join(cwd, "ffmpeg", ffmpegName)
		if _, err := os.Stat(localFFmpeg); err == nil {
			ffmpegPath = localFFmpeg
			return ffmpegPath
		}
	}

	// 3. 系统 PATH
	if path, err := exec.LookPath(ffmpegName); err == nil {
		ffmpegPath = path
		return ffmpegPath
	}

	// 默认使用 ffmpeg，让系统报错
	return ffmpegName
}

// adbPath 缓存 adb 可执行文件路径
var adbPath string

// findAdb 查找 adb 可执行文件
func findAdb() string {
	if adbPath != "" {
		return adbPath
	}

	adbName := "adb"
	if runtime.GOOS == "windows" {
		adbName = "adb.exe"
	}

	// 1. 程序同目录下的 adb 子目录
	if exePath, err := os.Executable(); err == nil {
		localAdb := filepath.Join(filepath.Dir(exePath), "adb", adbName)
		if _, err := os.Stat(localAdb); err == nil {
			adbPath = localAdb
			return adbPath
		}
	}

	// 2. 当前工作目录下的 adb 子目录
	if cwd, err := os.Getwd(); err == nil {
		localAdb := filepath.Join(cwd, "adb", adbName)
		if _, err := os.Stat(localAdb); err == nil {
			adbPath = localAdb
			return adbPath
		}
	}

	// 3. 系统 PATH
	if path, err := exec.LookPath(adbName); err == nil {
		adbPath = path
		return adbPath
	}

	// 默认使用 adb，让系统报错
	return adbName
}

// LogcatRecorder logcat 记录器
type LogcatRecorder struct {
	cmd      *exec.Cmd
	file     *os.File
	writer   *bufio.Writer
	mu       sync.Mutex
	stopped  bool
	done     chan struct{} // 用于等待写入 goroutine 完成
}

// StartLogcat 开始记录 logcat 到文件
func StartLogcat(outputPath string) (*LogcatRecorder, error) {
	// 创建输出文件
	file, err := os.Create(outputPath)
	if err != nil {
		return nil, fmt.Errorf("创建日志文件失败: %w", err)
	}

	// 先清除所有 logcat 缓存
	adb := findAdb()
	clearCmd := exec.Command(adb, "logcat", "-b", "all", "-c")
	hideWindow(clearCmd)
	clearCmd.Run()

	// 启动 logcat（读取所有缓冲区：main, system, radio, events, crash）
	cmd := exec.Command(adb, "logcat", "-b", "all")
	hideWindow(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("获取 logcat 输出失败: %w", err)
	}

	if err := cmd.Start(); err != nil {
		file.Close()
		return nil, fmt.Errorf("启动 logcat 失败: %w", err)
	}

	// 使用带缓冲的写入器提高性能（1MB 缓冲区，适合大日志）
	writer := bufio.NewWriterSize(file, 1024*1024) // 1MB 缓冲区

	recorder := &LogcatRecorder{
		cmd:    cmd,
		file:   file,
		writer: writer,
		done:   make(chan struct{}),
	}

	// 异步写入日志
	go func() {
		defer close(recorder.done)

		scanner := bufio.NewScanner(stdout)
		// 增大缓冲区以处理长行
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		for scanner.Scan() {
			line := scanner.Text()
			timestamp := time.Now().Format("2006-01-02 15:04:05.000")

			recorder.mu.Lock()
			if !recorder.stopped {
				fmt.Fprintf(writer, "[%s] %s\n", timestamp, line)
			}
			recorder.mu.Unlock()
		}
	}()

	return recorder, nil
}

// Stop 停止 logcat 记录
func (r *LogcatRecorder) Stop() error {
	// 先终止进程，这会导致 stdout 关闭，scanner.Scan() 返回 false
	if r.cmd.Process != nil {
		r.cmd.Process.Kill()
	}

	// 等待写入 goroutine 完成（最多等待 3 秒）
	select {
	case <-r.done:
	case <-time.After(3 * time.Second):
	}

	r.cmd.Wait()

	// 标记停止
	r.mu.Lock()
	r.stopped = true
	r.mu.Unlock()

	// 刷新缓冲区并确保数据写入磁盘
	if r.writer != nil {
		r.writer.Flush()
	}
	if r.file != nil {
		r.file.Sync()
		r.file.Close()
	}

	return nil
}

// adbMutex 用于序列化 ADB 截图操作，避免与 logcat 冲突
var adbMutex sync.Mutex

// Screenshot 截图并保存到本地（使用 screencap，速度快）
func Screenshot(outputPath string) error {
	var lastErr error

	// 重试最多3次
	for retry := 0; retry < 3; retry++ {
		if retry > 0 {
			time.Sleep(500 * time.Millisecond)
		}

		err := doScreenshot(outputPath)
		if err == nil {
			return nil
		}
		lastErr = err
	}

	return lastErr
}

// doScreenshot 使用 screencap 快速截图
func doScreenshot(outputPath string) error {
	adb := findAdb()

	// 设备上的临时截图路径
	devicePngPath := "/sdcard/screenshot_tmp.png"

	// 使用 screencap 截图（比 screenrecord 快得多）
	capCmd := exec.Command(adb, "shell", "screencap", "-p", devicePngPath)
	hideWindow(capCmd)
	if output, err := capCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("screencap 失败: %w, 输出: %s", err, string(output))
	}

	// 拉取截图到本地
	pullCmd := exec.Command(adb, "pull", devicePngPath, outputPath)
	hideWindow(pullCmd)
	if output, err := pullCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("拉取截图失败: %w, 输出: %s", err, string(output))
	}

	// 删除设备上的临时截图
	rmCmd := exec.Command(adb, "shell", "rm", "-f", devicePngPath)
	hideWindow(rmCmd)
	rmCmd.Run()

	return nil
}

// CheckDevice 检查 adb 设备连接
func CheckDevice() error {
	adb := findAdb()
	cmd := exec.Command(adb, "devices")
	hideWindow(cmd)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("adb 命令执行失败: %w", err)
	}

	// 检查是否有设备连接
	lines := string(output)
	if len(lines) < 30 {
		return fmt.Errorf("没有检测到 adb 设备")
	}

	return nil
}

// Connect 通过 IP 连接 ADB 设备
func Connect(ip string) (string, error) {
	adb := findAdb()

	// 默认端口 5555
	address := ip
	if !strings.Contains(ip, ":") {
		address = ip + ":5555"
	}

	cmd := exec.Command(adb, "connect", address)
	hideWindow(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("连接失败: %s", string(output))
	}

	result := string(output)
	if strings.Contains(result, "connected") {
		return fmt.Sprintf("已连接到 %s", address), nil
	} else if strings.Contains(result, "already connected") {
		return fmt.Sprintf("已连接到 %s", address), nil
	}

	return "", fmt.Errorf("连接失败: %s", result)
}

// VideoRecorder 视频录制器
type VideoRecorder struct {
	cmd            *exec.Cmd
	devicePath     string
	localPath      string
	mu             sync.Mutex
	stopped        bool
	maxDuration    int // 最大录制时长（秒）
}

// getAndroidVersion 获取 Android 版本号
func getAndroidVersion() int {
	adb := findAdb()
	cmd := exec.Command(adb, "shell", "getprop", "ro.build.version.sdk")
	hideWindow(cmd)
	output, err := cmd.Output()
	if err != nil {
		return 0
	}
	version := strings.TrimSpace(string(output))
	var v int
	fmt.Sscanf(version, "%d", &v)
	return v
}

// StartVideoRecording 开始录制屏幕视频
// outputPath: 本地保存路径
// maxDuration: 最大录制时长（秒），0 表示使用默认值 180 秒
func StartVideoRecording(outputPath string, maxDuration int) (*VideoRecorder, error) {
	adbMutex.Lock()
	defer adbMutex.Unlock()

	adb := findAdb()

	if maxDuration <= 0 {
		maxDuration = 180 // 默认最大 180 秒
	}

	// 设备上的临时视频路径
	devicePath := "/sdcard/recording_tmp.mp4"

	// 先删除可能存在的旧文件（设备和本地）
	rmCmd := exec.Command(adb, "shell", "rm", "-f", devicePath)
	hideWindow(rmCmd)
	rmCmd.Run()

	// 删除本地可能存在的旧文件和临时文件
	os.Remove(outputPath)
	os.Remove(outputPath + ".tmp")

	// 构建 screenrecord 命令参数
	args := []string{"shell", "screenrecord", "--time-limit", fmt.Sprintf("%d", maxDuration)}

	// Android 10+ (SDK 29+) 支持录制内部音频
	sdkVersion := getAndroidVersion()
	if sdkVersion >= 29 {
		// Android 10+ 使用 --bugreport 可以录制内部音频
		args = append(args, "--bugreport")
	}

	args = append(args, devicePath)

	// 启动 screenrecord（在后台运行）
	cmd := exec.Command(adb, args...)
	hideWindow(cmd)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动录制失败: %w", err)
	}

	recorder := &VideoRecorder{
		cmd:         cmd,
		devicePath:  devicePath,
		localPath:   outputPath,
		maxDuration: maxDuration,
	}

	return recorder, nil
}

// Stop 停止录制并拉取视频文件
func (r *VideoRecorder) Stop() error {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return nil
	}
	r.stopped = true
	r.mu.Unlock()

	adb := findAdb()

	// 发送中断信号停止录制
	// screenrecord 会在收到信号后正常结束并保存文件
	if r.cmd.Process != nil {
		// 通过 adb shell 发送 SIGINT 信号
		killCmd := exec.Command(adb, "shell", "pkill", "-SIGINT", "screenrecord")
		hideWindow(killCmd)
		killCmd.Run()

		// 等待进程结束
		r.cmd.Wait()
	}

	// 等待一小段时间确保文件写入完成
	time.Sleep(500 * time.Millisecond)

	// 使用临时文件路径拉取，完成后再重命名
	// 这样可以确保最终文件只在完全拉取后才出现
	tmpPath := r.localPath + ".tmp"
	pullCmd := exec.Command(adb, "pull", r.devicePath, tmpPath)
	hideWindow(pullCmd)
	if output, err := pullCmd.CombinedOutput(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("拉取视频失败: %w, 输出: %s", err, string(output))
	}

	// 重命名为最终文件名
	if err := os.Rename(tmpPath, r.localPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("重命名视频文件失败: %w", err)
	}

	// 删除设备上的临时视频
	rmCmd := exec.Command(adb, "shell", "rm", "-f", r.devicePath)
	hideWindow(rmCmd)
	rmCmd.Run()

	return nil
}

// IsRecording 检查是否正在录制
func (r *VideoRecorder) IsRecording() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.stopped
}

// StopScreenRecording 停止设备上正在运行的 screenrecord 进程
// 这是一个全局函数，用于在外部需要强制停止录制时调用
func StopScreenRecording() {
	adb := findAdb()

	// 发送 SIGINT 信号优雅停止 screenrecord，让它正确写入文件尾部
	killCmd := exec.Command(adb, "shell", "pkill", "-SIGINT", "screenrecord")
	hideWindow(killCmd)
	killCmd.Run()

	// 等待 screenrecord 进程结束并写入文件
	time.Sleep(1500 * time.Millisecond)

	// 再次尝试强制终止（如果还在运行）
	killCmd2 := exec.Command(adb, "shell", "pkill", "-9", "screenrecord")
	hideWindow(killCmd2)
	killCmd2.Run()
}

// StopAllAdbProcesses 停止所有 adb 相关的后台进程
// 用于在强制停止播放时清理所有相关资源
func StopAllAdbProcesses() {
	adb := findAdb()

	// 1. 优雅停止 screenrecord
	killCmd := exec.Command(adb, "shell", "pkill", "-SIGINT", "screenrecord")
	hideWindow(killCmd)
	killCmd.Run()

	// 等待 screenrecord 写入文件
	time.Sleep(1500 * time.Millisecond)

	// 2. 强制停止 screenrecord（如果还在运行）
	killCmd2 := exec.Command(adb, "shell", "pkill", "-9", "screenrecord")
	hideWindow(killCmd2)
	killCmd2.Run()

	// 3. 停止本机上的 adb logcat 进程
	// Windows 上使用 taskkill，Linux 上使用 pkill
	if runtime.GOOS == "windows" {
		// 终止所有 adb.exe 进程中正在执行 logcat 的
		// 注意：这会终止所有 adb logcat，但通常这是预期行为
		killLogcat := exec.Command("taskkill", "/F", "/IM", "adb.exe", "/FI", "WINDOWTITLE eq *logcat*")
		hideWindow(killLogcat)
		killLogcat.Run()
	} else {
		killLogcat := exec.Command("pkill", "-f", "adb.*logcat")
		killLogcat.Run()
	}
}
