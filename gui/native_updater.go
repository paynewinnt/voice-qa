package main

import (
	"archive/zip"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	nativeUpdateModeFlag      = "--voice-qa-apply-update"
	nativeUpdaterCleanupFlag  = "--voice-qa-cleanup-updater"
	nativeUpdaterFilePrefix   = "voice-qa-native-updater-"
	maxUpdateArchiveEntries   = 20000
	maxUpdateExtractedBytes   = int64(2 << 30)
	updateParentExitTimeout   = 2 * time.Minute
	updateFileRetryTimeout    = 30 * time.Second
	updateFileRetryInterval   = 300 * time.Millisecond
	updateRestartCheckDelay   = 1200 * time.Millisecond
	updateShutdownSettleDelay = 500 * time.Millisecond
)

var preservedUpdateNames = []string{"output", "config.json", "text.txt"}

type nativeUpdaterOptions struct {
	ZipPath    string
	InstallDir string
	ExeName    string
	ParentPID  int
	HelperPath string
}

type nativeUpdaterHooks struct {
	waitForProcessExit func(int, time.Duration) error
	startApplication   func(string, string, string) (int, error)
	processRunning     func(int) (bool, error)
	sleep              func(time.Duration)
}

type nativeUpdateLogger struct {
	mu   sync.Mutex
	file *os.File
	path string
}

type managedUpdateEntry struct {
	targetPath     string
	backupPath     string
	hadOriginal    bool
	replaceStarted bool
}

type updateTransaction struct {
	installDir string
	backupDir  string
	entries    []managedUpdateEntry
	logger     *nativeUpdateLogger
}

func runNativeUpdaterMode(args []string) (int, bool) {
	if len(args) == 0 || args[0] != nativeUpdateModeFlag {
		return 0, false
	}
	options, err := parseNativeUpdaterOptions(args[1:])
	if err != nil {
		appendNativeUpdaterStartupError(err)
		return 2, true
	}
	if err := runNativeUpdater(options, defaultNativeUpdaterHooks()); err != nil {
		return 1, true
	}
	return 0, true
}

func parseNativeUpdaterOptions(args []string) (nativeUpdaterOptions, error) {
	flags := flag.NewFlagSet("voice-qa-native-updater", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	zipPath := flags.String("zip", "", "validated update archive")
	installDir := flags.String("install-dir", "", "application directory")
	exeName := flags.String("exe-name", "", "application executable name")
	parentPID := flags.Int("parent-pid", 0, "application process id")
	if err := flags.Parse(args); err != nil {
		return nativeUpdaterOptions{}, fmt.Errorf("解析更新参数失败: %w", err)
	}
	if flags.NArg() != 0 {
		return nativeUpdaterOptions{}, fmt.Errorf("更新参数包含无法识别的内容")
	}

	normalizedZip, err := normalizeExistingUpdatePath(*zipPath)
	if err != nil {
		return nativeUpdaterOptions{}, err
	}
	normalizedInstallDir, err := filepath.Abs(strings.TrimSpace(*installDir))
	if err != nil {
		return nativeUpdaterOptions{}, fmt.Errorf("解析程序目录失败: %w", err)
	}
	info, err := os.Stat(normalizedInstallDir)
	if err != nil || !info.IsDir() {
		return nativeUpdaterOptions{}, fmt.Errorf("程序目录不存在或不可访问")
	}
	normalizedExeName := strings.TrimSpace(*exeName)
	if normalizedExeName == "" || filepath.Base(normalizedExeName) != normalizedExeName ||
		!strings.EqualFold(filepath.Ext(normalizedExeName), ".exe") {
		return nativeUpdaterOptions{}, fmt.Errorf("程序文件名无效")
	}
	if *parentPID <= 0 {
		return nativeUpdaterOptions{}, fmt.Errorf("主程序进程 ID 无效")
	}
	helperPath, err := os.Executable()
	if err != nil {
		return nativeUpdaterOptions{}, fmt.Errorf("获取原生更新程序路径失败: %w", err)
	}
	helperPath, err = filepath.Abs(helperPath)
	if err != nil {
		return nativeUpdaterOptions{}, fmt.Errorf("解析原生更新程序路径失败: %w", err)
	}
	return nativeUpdaterOptions{
		ZipPath:    normalizedZip,
		InstallDir: filepath.Clean(normalizedInstallDir),
		ExeName:    normalizedExeName,
		ParentPID:  *parentPID,
		HelperPath: helperPath,
	}, nil
}

func defaultNativeUpdaterHooks() nativeUpdaterHooks {
	return nativeUpdaterHooks{
		waitForProcessExit: waitForUpdateProcessExit,
		startApplication:   startNativeUpdatedApplication,
		processRunning:     updateProcessRunning,
		sleep:              time.Sleep,
	}
}

func runNativeUpdater(options nativeUpdaterOptions, hooks nativeUpdaterHooks) error {
	logger := openNativeUpdateLogger(options.InstallDir)
	defer logger.Close()
	logger.Printf("原生更新程序已启动，更新包: %s", options.ZipPath)
	logger.Printf("等待主程序退出，PID: %d", options.ParentPID)

	if err := hooks.waitForProcessExit(options.ParentPID, updateParentExitTimeout); err != nil {
		logger.Printf("等待主程序退出失败: %v", err)
		return err
	}
	hooks.sleep(updateShutdownSettleDelay)

	transaction, err := installNativeUpdateArchive(options.ZipPath, options.InstallDir, options.ExeName, logger)
	if err != nil {
		logger.Printf("替换程序文件失败: %v", err)
		if restartErr := restartInstalledApplication(options, hooks, logger); restartErr != nil {
			return combineNativeUpdaterErrors(err, fmt.Errorf("重新打开旧版本失败: %w", restartErr))
		}
		return err
	}

	installedExe := filepath.Join(options.InstallDir, options.ExeName)
	newPID, err := hooks.startApplication(installedExe, options.InstallDir, options.HelperPath)
	if err != nil {
		logger.Printf("启动新版本失败: %v", err)
		return rollbackAndRestart(options, transaction, hooks, logger, err)
	}
	hooks.sleep(updateRestartCheckDelay)
	running, checkErr := hooks.processRunning(newPID)
	if checkErr != nil {
		logger.Printf("无法确认新版本运行状态，将保留本次更新: %v", checkErr)
	} else if !running {
		err = fmt.Errorf("新版本启动后立即退出")
		logger.Printf("%v", err)
		return rollbackAndRestart(options, transaction, hooks, logger, err)
	}

	if err := transaction.commit(); err != nil {
		logger.Printf("清理更新备份失败，可稍后手动删除 %s: %v", transaction.backupDir, err)
	}
	if err := removePathWithRetry(options.ZipPath); err != nil {
		logger.Printf("删除已安装更新包失败，可稍后手动删除: %v", err)
	}
	logger.Printf("更新安装成功，新版本进程 PID: %d", newPID)
	return nil
}

func rollbackAndRestart(options nativeUpdaterOptions, transaction *updateTransaction, hooks nativeUpdaterHooks, logger *nativeUpdateLogger, cause error) error {
	rollbackErr := transaction.rollback()
	if rollbackErr != nil {
		logger.Printf("回滚旧版本失败: %v", rollbackErr)
	} else {
		logger.Printf("程序文件已回滚到旧版本")
	}
	restartErr := restartInstalledApplication(options, hooks, logger)
	if restartErr != nil {
		logger.Printf("重新打开旧版本失败: %v", restartErr)
	}
	return combineNativeUpdaterErrors(cause, rollbackErr, restartErr)
}

func restartInstalledApplication(options nativeUpdaterOptions, hooks nativeUpdaterHooks, logger *nativeUpdateLogger) error {
	executablePath := filepath.Join(options.InstallDir, options.ExeName)
	if info, err := os.Stat(executablePath); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("旧版本程序文件不存在")
	}
	pid, err := hooks.startApplication(executablePath, options.InstallDir, options.HelperPath)
	if err != nil {
		return err
	}
	logger.Printf("已重新打开原路径下的程序，PID: %d", pid)
	return nil
}

func startNativeUpdatedApplication(executablePath, installDir, helperPath string) (int, error) {
	args := make([]string, 0, 2)
	if validNativeUpdaterCleanupPath(helperPath) {
		args = append(args, nativeUpdaterCleanupFlag, helperPath)
	}
	cmd := exec.Command(executablePath, args...)
	cmd.Dir = installDir
	hideConsoleWindow(cmd)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	return pid, nil
}

func installNativeUpdateArchive(zipPath, installDir, exeName string, logger *nativeUpdateLogger) (*updateTransaction, error) {
	stageDir, sourceRoot, err := extractNativeUpdateArchive(zipPath)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stageDir)

	sourceExe, err := findArchiveExecutable(sourceRoot, exeName)
	if err != nil {
		return nil, err
	}
	backupDir, err := os.MkdirTemp(installDir, ".voice-qa-update-backup-*")
	if err != nil {
		return nil, fmt.Errorf("创建更新备份目录失败: %w", err)
	}
	transaction := &updateTransaction{
		installDir: installDir,
		backupDir:  backupDir,
		logger:     logger,
	}

	items, err := os.ReadDir(sourceRoot)
	if err != nil {
		_ = os.RemoveAll(backupDir)
		return nil, fmt.Errorf("读取更新包内容失败: %w", err)
	}
	seenTargets := make(map[string]struct{}, len(items))
	for _, item := range items {
		sourcePath := filepath.Join(sourceRoot, item.Name())
		targetName := item.Name()
		if samePath(sourcePath, sourceExe) {
			targetName = exeName
		}
		if strings.HasPrefix(strings.ToLower(targetName), ".voice-qa-update-backup-") {
			err = fmt.Errorf("更新包包含保留的内部目录名: %s", targetName)
			break
		}
		targetKey := strings.ToLower(targetName)
		if _, exists := seenTargets[targetKey]; exists {
			err = fmt.Errorf("更新包包含重复的顶层文件名: %s", targetName)
			break
		}
		seenTargets[targetKey] = struct{}{}

		existingPath, exists, findErr := findExistingChild(installDir, targetName)
		if findErr != nil {
			err = findErr
			break
		}
		if isPreservedUpdateName(targetName) && exists {
			logger.Printf("保留用户数据: %s", targetName)
			continue
		}

		if err = transaction.installPath(sourcePath, existingPath, filepath.Join(backupDir, targetName)); err != nil {
			break
		}
		logger.Printf("已替换程序文件: %s", targetName)
	}
	if err == nil {
		installedExe := filepath.Join(installDir, exeName)
		if info, statErr := os.Stat(installedExe); statErr != nil || !info.Mode().IsRegular() {
			err = fmt.Errorf("更新后的程序文件不存在: %s", exeName)
		}
	}
	if err == nil {
		return transaction, nil
	}

	rollbackErr := transaction.rollback()
	if rollbackErr != nil {
		return nil, combineNativeUpdaterErrors(err, fmt.Errorf("回滚失败: %w", rollbackErr))
	}
	return nil, err
}

// Merge directories, replacing only files carried by the release archive.
func (transaction *updateTransaction) installPath(source, target, backup string) error {
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return err
	}
	targetInfo, err := os.Lstat(target)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if exists && (targetInfo.Mode()&os.ModeSymlink != 0 || sourceInfo.IsDir() != targetInfo.IsDir()) {
		return fmt.Errorf("更新路径类型冲突，已保留原文件: %s", target)
	}
	if sourceInfo.IsDir() {
		if !exists {
			if err := os.Mkdir(target, 0755); err != nil {
				return err
			}
			transaction.entries = append(transaction.entries, managedUpdateEntry{targetPath: target, replaceStarted: true})
		}
		children, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, child := range children {
			childTarget, _, err := findExistingChild(target, child.Name())
			if err != nil {
				return err
			}
			if err := transaction.installPath(filepath.Join(source, child.Name()), childTarget, filepath.Join(backup, child.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if !sourceInfo.Mode().IsRegular() || (exists && !targetInfo.Mode().IsRegular()) {
		return fmt.Errorf("更新路径不是普通文件: %s", target)
	}
	entry := managedUpdateEntry{targetPath: target, backupPath: backup}
	transaction.entries = append(transaction.entries, entry)
	index := len(transaction.entries) - 1
	if exists {
		if err := os.MkdirAll(filepath.Dir(backup), 0755); err != nil {
			return err
		}
		if err := movePathWithRetry(target, backup); err != nil {
			return fmt.Errorf("备份 %s 失败: %w", target, err)
		}
		transaction.entries[index].hadOriginal = true
	}
	transaction.entries[index].replaceStarted = true
	if err := copyPathWithRetry(source, target); err != nil {
		return fmt.Errorf("安装 %s 失败: %w", target, err)
	}
	return nil
}

func (transaction *updateTransaction) rollback() error {
	var rollbackErrors []error
	for index := len(transaction.entries) - 1; index >= 0; index-- {
		entry := transaction.entries[index]
		if entry.replaceStarted {
			if err := removePathWithRetry(entry.targetPath); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("删除新文件 %s 失败: %w", filepath.Base(entry.targetPath), err))
				continue
			}
		}
		if entry.hadOriginal {
			if err := movePathWithRetry(entry.backupPath, entry.targetPath); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("恢复 %s 失败: %w", filepath.Base(entry.targetPath), err))
			}
		}
	}
	if len(rollbackErrors) == 0 {
		if err := removePathWithRetry(transaction.backupDir); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
	}
	return combineNativeUpdaterErrors(rollbackErrors...)
}

func (transaction *updateTransaction) commit() error {
	return removePathWithRetry(transaction.backupDir)
}

func extractNativeUpdateArchive(zipPath string) (stageDir string, sourceRoot string, resultErr error) {
	archive, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", "", fmt.Errorf("打开更新包失败: %w", err)
	}
	defer archive.Close()
	if len(archive.File) == 0 {
		return "", "", fmt.Errorf("更新包为空")
	}
	if len(archive.File) > maxUpdateArchiveEntries {
		return "", "", fmt.Errorf("更新包文件数量超过限制")
	}

	stageDir, err = os.MkdirTemp("", "voice-qa-update-stage-*")
	if err != nil {
		return "", "", fmt.Errorf("创建更新解压目录失败: %w", err)
	}
	defer func() {
		if resultErr != nil {
			_ = os.RemoveAll(stageDir)
		}
	}()

	var rootName string
	var extractedBytes int64
	seenPaths := make(map[string]struct{}, len(archive.File))
	for _, archivedFile := range archive.File {
		normalizedName, parts, isDirectory, err := validateUpdateArchivePath(archivedFile)
		if err != nil {
			return "", "", err
		}
		if rootName == "" {
			rootName = parts[0]
		} else if !strings.EqualFold(rootName, parts[0]) {
			return "", "", fmt.Errorf("更新包必须且只能包含一个顶层目录")
		}
		if len(parts) == 1 && !isDirectory {
			return "", "", fmt.Errorf("更新包根目录不能直接包含文件")
		}
		key := strings.ToLower(normalizedName)
		if _, exists := seenPaths[key]; exists {
			return "", "", fmt.Errorf("更新包包含重复路径: %s", normalizedName)
		}
		seenPaths[key] = struct{}{}
		if archivedFile.Mode()&os.ModeSymlink != 0 {
			return "", "", fmt.Errorf("更新包不允许包含符号链接: %s", normalizedName)
		}

		targetPath := filepath.Join(stageDir, filepath.FromSlash(normalizedName))
		if !pathInsideDirectory(stageDir, targetPath) {
			return "", "", fmt.Errorf("更新包包含越界路径: %s", normalizedName)
		}
		if isDirectory {
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				return "", "", fmt.Errorf("创建解压目录失败: %w", err)
			}
			continue
		}
		if archivedFile.UncompressedSize64 > uint64(maxUpdateExtractedBytes-extractedBytes) {
			return "", "", fmt.Errorf("更新包解压后大小超过限制")
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return "", "", fmt.Errorf("创建解压目录失败: %w", err)
		}
		input, err := archivedFile.Open()
		if err != nil {
			return "", "", fmt.Errorf("读取压缩文件 %s 失败: %w", normalizedName, err)
		}
		mode := archivedFile.Mode().Perm()
		if mode == 0 {
			mode = 0644
		}
		output, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			_ = input.Close()
			return "", "", fmt.Errorf("创建解压文件 %s 失败: %w", normalizedName, err)
		}
		remaining := maxUpdateExtractedBytes - extractedBytes
		written, copyErr := io.Copy(output, io.LimitReader(input, remaining+1))
		inputCloseErr := input.Close()
		outputCloseErr := output.Close()
		if copyErr != nil {
			return "", "", fmt.Errorf("解压文件 %s 失败: %w", normalizedName, copyErr)
		}
		if inputCloseErr != nil {
			return "", "", fmt.Errorf("关闭压缩文件 %s 失败: %w", normalizedName, inputCloseErr)
		}
		if outputCloseErr != nil {
			return "", "", fmt.Errorf("关闭解压文件 %s 失败: %w", normalizedName, outputCloseErr)
		}
		if written > remaining {
			return "", "", fmt.Errorf("更新包解压后大小超过限制")
		}
		if uint64(written) != archivedFile.UncompressedSize64 {
			return "", "", fmt.Errorf("更新包文件 %s 数据不完整", normalizedName)
		}
		extractedBytes += written
	}

	if rootName == "" {
		return "", "", fmt.Errorf("更新包缺少顶层目录")
	}
	sourceRoot = filepath.Join(stageDir, filepath.FromSlash(rootName))
	info, err := os.Stat(sourceRoot)
	if err != nil || !info.IsDir() {
		return "", "", fmt.Errorf("更新包顶层目录无效")
	}
	return stageDir, sourceRoot, nil
}

func validateUpdateArchivePath(archivedFile *zip.File) (string, []string, bool, error) {
	rawName := strings.ReplaceAll(archivedFile.Name, "\\", "/")
	isDirectory := archivedFile.FileInfo().IsDir() || strings.HasSuffix(rawName, "/")
	normalizedName := path.Clean(rawName)
	if normalizedName == "." || path.IsAbs(normalizedName) || strings.HasPrefix(normalizedName, "../") {
		return "", nil, false, fmt.Errorf("更新包包含无效路径: %s", archivedFile.Name)
	}
	parts := strings.Split(normalizedName, "/")
	for _, segment := range parts {
		if err := validateUpdateArchiveSegment(segment); err != nil {
			return "", nil, false, fmt.Errorf("更新包路径 %s 无效: %w", archivedFile.Name, err)
		}
	}
	return normalizedName, parts, isDirectory, nil
}

func validateUpdateArchiveSegment(segment string) error {
	if segment == "" || segment == "." || segment == ".." {
		return fmt.Errorf("包含空目录或相对目录")
	}
	if strings.ContainsAny(segment, "<>:\"|?*\x00") {
		return fmt.Errorf("包含 Windows 不支持的字符")
	}
	if strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " ") {
		return fmt.Errorf("目录或文件名以点或空格结尾")
	}
	stem := strings.ToUpper(strings.SplitN(segment, ".", 2)[0])
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return fmt.Errorf("使用了 Windows 保留名称")
	}
	return nil
}

func findArchiveExecutable(sourceRoot, exeName string) (string, error) {
	if expectedPath, exists, err := findExistingChild(sourceRoot, exeName); err != nil {
		return "", err
	} else if exists {
		info, err := os.Stat(expectedPath)
		if err == nil && info.Mode().IsRegular() {
			return expectedPath, nil
		}
	}
	items, err := os.ReadDir(sourceRoot)
	if err != nil {
		return "", fmt.Errorf("读取更新包顶层目录失败: %w", err)
	}
	var executables []string
	for _, item := range items {
		if !item.IsDir() && strings.EqualFold(filepath.Ext(item.Name()), ".exe") {
			executables = append(executables, filepath.Join(sourceRoot, item.Name()))
		}
	}
	if len(executables) != 1 {
		return "", fmt.Errorf("更新包未包含 %s，且顶层 EXE 数量不是 1", exeName)
	}
	return executables[0], nil
}

func findExistingChild(directory, name string) (string, bool, error) {
	items, err := os.ReadDir(directory)
	if err != nil {
		return "", false, fmt.Errorf("读取目录 %s 失败: %w", directory, err)
	}
	for _, item := range items {
		if strings.EqualFold(item.Name(), name) {
			return filepath.Join(directory, item.Name()), true, nil
		}
	}
	return filepath.Join(directory, name), false, nil
}

func isPreservedUpdateName(name string) bool {
	for _, preservedName := range preservedUpdateNames {
		if strings.EqualFold(name, preservedName) {
			return true
		}
	}
	return false
}

func copyPathWithRetry(sourcePath, targetPath string) error {
	return retryNativeUpdateFileOperation(func() error {
		_ = os.RemoveAll(targetPath)
		return copyNativeUpdatePath(sourcePath, targetPath)
	})
}

func copyNativeUpdatePath(sourcePath, targetPath string) error {
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("不允许复制符号链接")
	}
	if info.IsDir() {
		if err := os.MkdirAll(targetPath, info.Mode().Perm()); err != nil {
			return err
		}
		items, err := os.ReadDir(sourcePath)
		if err != nil {
			return err
		}
		for _, item := range items {
			if err := copyNativeUpdatePath(filepath.Join(sourcePath, item.Name()), filepath.Join(targetPath, item.Name())); err != nil {
				return err
			}
		}
		return nil
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("不支持的文件类型")
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return err
	}
	input, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	output, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		_ = input.Close()
		return err
	}
	_, copyErr := io.Copy(output, input)
	inputCloseErr := input.Close()
	outputCloseErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if inputCloseErr != nil {
		return inputCloseErr
	}
	return outputCloseErr
}

func movePathWithRetry(sourcePath, targetPath string) error {
	return retryNativeUpdateFileOperation(func() error {
		if _, err := os.Stat(sourcePath); os.IsNotExist(err) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return err
		}
		return os.Rename(sourcePath, targetPath)
	})
}

func removePathWithRetry(targetPath string) error {
	return retryNativeUpdateFileOperation(func() error {
		err := os.RemoveAll(targetPath)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	})
}

func retryNativeUpdateFileOperation(operation func() error) error {
	deadline := time.Now().Add(updateFileRetryTimeout)
	var lastErr error
	for {
		if err := operation(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		time.Sleep(updateFileRetryInterval)
	}
}

func samePath(first, second string) bool {
	firstAbs, firstErr := filepath.Abs(first)
	secondAbs, secondErr := filepath.Abs(second)
	if firstErr != nil || secondErr != nil {
		return strings.EqualFold(filepath.Clean(first), filepath.Clean(second))
	}
	return strings.EqualFold(filepath.Clean(firstAbs), filepath.Clean(secondAbs))
}

func pathInsideDirectory(directory, targetPath string) bool {
	relativePath, err := filepath.Rel(directory, targetPath)
	if err != nil {
		return false
	}
	return relativePath != ".." && !strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) && !filepath.IsAbs(relativePath)
}

func combineNativeUpdaterErrors(errs ...error) error {
	messages := make([]string, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			messages = append(messages, err.Error())
		}
	}
	if len(messages) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(messages, "；"))
}

func openNativeUpdateLogger(installDir string) *nativeUpdateLogger {
	paths := []string{
		filepath.Join(installDir, "output", "update-install.log"),
		filepath.Join(os.TempDir(), "voice-qa-update-install.log"),
	}
	for _, logPath := range paths {
		if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
			continue
		}
		file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err == nil {
			return &nativeUpdateLogger{file: file, path: logPath}
		}
	}
	return &nativeUpdateLogger{}
}

func (logger *nativeUpdateLogger) Printf(format string, args ...interface{}) {
	if logger == nil || logger.file == nil {
		return
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()
	message := fmt.Sprintf(format, args...)
	_, _ = fmt.Fprintf(logger.file, "[%s] %s\r\n", time.Now().Format("2006-01-02 15:04:05"), message)
	_ = logger.file.Sync()
}

func (logger *nativeUpdateLogger) Close() {
	if logger == nil || logger.file == nil {
		return
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()
	_ = logger.file.Close()
	logger.file = nil
}

func appendNativeUpdaterStartupError(err error) {
	logger := openNativeUpdateLogger(os.TempDir())
	logger.Printf("原生更新程序参数错误: %v", err)
	logger.Close()
}

func validNativeUpdaterCleanupPath(helperPath string) bool {
	if strings.TrimSpace(helperPath) == "" {
		return false
	}
	absolutePath, err := filepath.Abs(helperPath)
	if err != nil {
		return false
	}
	tempDir, err := filepath.Abs(os.TempDir())
	if err != nil || !pathInsideDirectory(tempDir, absolutePath) {
		return false
	}
	baseName := strings.ToLower(filepath.Base(absolutePath))
	return strings.HasPrefix(baseName, nativeUpdaterFilePrefix) && strings.HasSuffix(baseName, ".exe")
}

func nativeUpdaterCleanupPath(args []string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == nativeUpdaterCleanupFlag && validNativeUpdaterCleanupPath(args[index+1]) {
			absolutePath, err := filepath.Abs(args[index+1])
			if err == nil {
				return absolutePath
			}
		}
	}
	return ""
}

func scheduleNativeUpdaterCleanup(args []string) {
	helperPath := nativeUpdaterCleanupPath(args)
	if helperPath == "" {
		return
	}
	go func() {
		for attempt := 0; attempt < 60; attempt++ {
			time.Sleep(500 * time.Millisecond)
			if err := os.Remove(helperPath); err == nil || os.IsNotExist(err) {
				return
			}
		}
	}()
}
