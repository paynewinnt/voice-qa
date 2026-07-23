# Windows GUI 局域网更新

Windows GUI 默认使用独立公网云服务器。需要在局域网内分发或加速下载时，可以在「软件更新」页手动填写以下版本清单地址：

```text
http://172.16.15.15/latest.json
```

GUI 会显示下载进度并校验 SHA256。校验完成后会启动独立更新器，保留 `output/`、`config.json`、`text.txt` 和其他用户文件，覆盖程序文件并自动重启；安装失败时会自动回滚。

## 1. Windows 服务器开启 IIS 静态文件服务

以下示例服务器 IP 为 `172.16.15.15`，Tailscale IP 为 `100.121.0.1`，站点目录为：

```text
D:\Devlop\GoProject\src\voice-qa-update
```

在 Windows 服务器上以管理员身份打开 PowerShell，安装 IIS 静态内容组件：

```powershell
Enable-WindowsOptionalFeature -Online -FeatureName IIS-WebServerRole,IIS-WebServer,IIS-CommonHttpFeatures,IIS-StaticContent -All
```

创建更新目录：

```powershell
New-Item -ItemType Directory -Force D:\Devlop\GoProject\src\voice-qa-update
```

创建 IIS 站点：

```powershell
Import-Module WebAdministration

New-Website `
  -Name "tts" `
  -PhysicalPath "D:\Devlop\GoProject\src\voice-qa-update" `
  -IPAddress "172.16.15.15" `
  -Port 80 `
  -Force
```

如果需要通过 Tailscale IP 访问，也添加 Tailscale 绑定：

```powershell
New-WebBinding -Name "tts" -Protocol http -IPAddress "100.121.0.1" -Port 80
```

如果 IIS 站点绑定里带了主机名（例如 `172.16.15.15:80:G`），直接访问 `http://172.16.15.15/latest.json` 可能会返回 404。建议确保存在无主机名绑定：

```powershell
New-WebBinding -Name "tts" -Protocol http -IPAddress "172.16.15.15" -Port 80
```

查看站点绑定和物理路径：

```powershell
Get-WebBinding -Name "tts" | Select-Object protocol,bindingInformation
Get-Website -Name "tts" | Select-Object Name,State,PhysicalPath
```

放行防火墙 80 端口：

```powershell
New-NetFirewallRule -DisplayName "Voice QA Update HTTP" -Direction Inbound -Protocol TCP -LocalPort 80 -Action Allow
```

如果 `.json` 不能正常返回，确认 IIS 有 JSON MIME 类型：

```powershell
Add-WebConfigurationProperty `
  -PSPath 'IIS:\' `
  -Filter 'system.webServer/staticContent' `
  -Name '.' `
  -Value @{fileExtension='.json';mimeType='application/json'}
```

## 2. 放置更新文件

在 IIS 站点目录中放置 `latest.json` 和对应的 GUI zip 包，例如：

```text
latest.json
tts-gui-windows-amd64-v2026.0428.1736.zip
```

`latest.json` 示例：

```json
{
  "version": "2026.0428.1736",
  "notes": "软件更新入口移到独立 tab，并在标题旁增加 NEW 快捷入口；同版本也可重新下载当前版本。",
  "url": "tts-gui-windows-amd64-v2026.0428.1736.zip",
  "sha256": "9058e68554d66be63891506f28d25ad36e67d2e26edcbafbbcaa5e3618e5867e"
}
```

`url` 可以是相对路径，也可以是完整 URL。相对路径会以 `latest.json` 所在地址为基准解析：

```json
{
  "url": "http://172.16.15.15/tts-gui-windows-amd64-v2026.0428.1736.zip"
}
```

生成 zip 的 SHA256：

```powershell
Get-FileHash D:\Devlop\GoProject\src\voice-qa-update\tts-gui-windows-amd64-v2026.0428.1736.zip -Algorithm SHA256
```

`sha256` 可留空；填写后 GUI 下载完成会校验，不一致会删除临时下载文件并提示失败。

## 3. 验证服务可访问

在服务器本机验证：

```powershell
Invoke-WebRequest http://127.0.0.1/latest.json -UseBasicParsing
Invoke-WebRequest http://172.16.15.15/latest.json -UseBasicParsing
```

在局域网其它电脑浏览器访问：

```text
http://172.16.15.15/latest.json
```

如通过 Tailscale 访问：

```text
http://100.121.0.1/latest.json
```

正常时应返回 `latest.json` 内容；zip 地址应返回 `200 OK` 并能下载。

## 4. GUI 操作流程

1. 打开「软件更新」tab，或点击标题旁的 `NEW` 快捷入口。
2. 在版本清单地址中填写 `http://172.16.15.15/latest.json`。
3. 点击「检查更新」。
4. 有新版本时点击「下载更新包」。如果版本相同，也可以点击「重新下载当前版本」。
5. 等待下载进度达到 100%，程序会自动退出、覆盖程序文件并重启。
6. 更新会保留 `output/`、`config.json`、`text.txt`；不需要手动删除旧包或解压 zip。

构建正式包时会把构建版本注入 GUI，用于和 `latest.json` 的 `version` 比较。版本格式建议继续使用 `YYYY.MMDD.HHMM`。

## 5. 常见问题

- `latest.json` 返回 404：检查 IIS 站点物理路径是否就是放置 `latest.json` 的目录。
- IP 直连 404：检查站点绑定是否带了主机名；需要无主机名绑定，例如 `172.16.15.15:80:`。
- 服务器本机能访问，局域网不能访问：检查 Windows 防火墙是否放行 TCP 80。
- GUI 提示 SHA256 校验失败：重新计算 zip 的 SHA256 并更新 `latest.json`。
- GUI 显示当前已是最新版本：`latest.json` 的 `version` 没有高于当前 GUI 版本；同版本可重新下载当前版本。
- 自动安装失败：查看 `output/update-install.log`。更新器会尝试回滚并重新打开旧版本，失败的 zip 会保留在当前程序目录的上一级或 `output/updates` 中供排查。
