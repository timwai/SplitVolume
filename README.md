# SplitVolume V5 · 分卷压缩助手

Windows 桌面端的文件 / 文件夹分卷压缩工具，默认生成跨平台的 `tar.gz.001`、`.002` … 分卷，Linux 无需 7z 即可还原。压缩引擎由 Go 实现，离线运行。

## V5：重构紧凑界面与浏览器启动隔离

本次重构针对 V4 反馈的四类问题：**窗口过大、页面滚动条、布局过度拥挤、Edge 的首次运行 / 扩展弹窗**。

- **窗口尺寸**：从 V4 的约 `1160×780` 缩小为 **`960×630`**（以逻辑像素为目标，系统任务栏及 DPI 设置会影响实际显示）。窗口可以调整大小，遇到较小的工作区会自动收缩。
- **重新布局**：主界面只保留文件清单、输出与压缩配置、概要统计和底部任务进度；取消持续占用空间的大图表和运行日志。点击右上角「统计」「日志」或文件卡片的「查看图表」可打开详情弹层。内容不会因简单缩放而拥挤。
- **避免窗口整体滚动**：主页面占满可用高度，文件清单仅在项目较多时在**文件表格内部滚动**；帮助和日志也只在各自的内容区域滚动。窗口宽度不足 `750px` 时，使用「文件列表 / 压缩设置」切换，避免左右控件被挤到无法操作。
- **减少 Edge 干扰**：**依然使用 Edge 应用模式渲染 UI**，但每次启动都创建一个全新隔离的临时用户配置，禁用所有扩展、同步、后台模式与首次运行 / 崩溃恢复提示。**不使用你日常 Edge 的用户资料或插件，因此不会由该配置主动启动已安装的扩展页面。** 仍需 Windows 安装 Edge；如果设备策略强制接管 Edge 或弹系统级提示，不能保证彻底消除，需要改用内嵌 WebView2 / 原生 UI 的后续版本。
- **独立帮助**：「帮助」页面采用侧边方案切换，支持 Linux 直接解压、无 tar/gzip/7z 的独立解压器、先合并再解压、传统 7z 四种命令及一键复制。
- **原有能力保留**：文件和文件夹批量导入、Windows 原生文件选择、流式分卷压缩、可选 7z、取消清理、完整性校验、进度、真实分卷图表、Linux AMD64/ARM64/ARMv7 静态解压器。

> 注意：浏览器拖放文件会经本机私有缓存复制一份，因此数 GB 的文件建议点击「添加文件」选择，避免重复占用临时磁盘空间。这不影响直接添加文件/文件夹的性能。

## Windows 使用

双击 `SplitVolume.exe`（x64）或 `SplitVolume-Windows-ARM64.exe`（ARM64）；无需 Python、Node、7-Zip（默认 tar.gz 模式）。Windows 10 / 11 需有 Microsoft Edge，界面和压缩不需要网络访问。

1. 添加文件或文件夹，也可将文件拖放到界面。
2. 选择输出目录，设定压缩包名、每卷大小（MiB）、压缩等级。
3. 默认保持「tar.gz 分卷 · Linux 推荐」，选择性勾选压缩后的完整性校验。
4. 点击「开始压缩」；用「统计」查看分卷环形图 / 柱状图，用「日志」查看详细过程。
5. 完成后点击「打开输出目录」。

示例结果：

```text
backup.tar.gz.001
backup.tar.gz.002
backup.tar.gz.003
```

**请保留完整、连续的所有分卷**。不能单独解压 `.002` 等后续卷。

## Linux 解压（无 7z）

若有常见的 `cat` / `tar`：

```sh
mkdir -p output
cat backup.tar.gz.??? | tar -xz -C output
```

这个通配符适用于 `.001`—`.999`，超过 999 卷请使用附带的独立解压器（自动正确拼接任意长度的编号）。

若 Linux **没有 `7z`、`tar`、`gzip` 和 Python**，在匹配架构的解压程序与 `extract.sh` 所在目录执行：

```sh
chmod +x ./extract-linux-*
sh ./extract.sh backup.tar.gz.001 ./output
```

也可直接运行 `./extract-linux-amd64 backup.tar.gz.001 ./output`（Linux ARM64 则是 `extract-linux-arm64`，32 位 ARMv7 则是 `extract-linux-armv7`）。这些程序使用 Go 静态编译，无需安装第三方运行库。对于自选的 `7z` 格式，Linux 上仍需 `7z x backup.7z.001 -ooutput`。详见 `解压帮助.md`。

## 构建 / 验证

要求 Go 1.22+：

```sh
go test ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags='-H windowsgui -s -w' -o SplitVolume.exe ./cmd/split7z
GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build -ldflags='-H windowsgui -s -w' -o SplitVolume-Windows-ARM64.exe ./cmd/split7z
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags='-s -w' -o extract-linux-amd64 ./cmd/extract
```

Windows 上也可执行 `./build.ps1`，一键编译 Windows x64 / ARM64 及 Linux AMD64 / ARM64 / ARMv7。源码：`cmd/split7z/web/` 为紧凑 UI（HTML/CSS/JS）；`cmd/split7z/main_windows.go` 为隔离式 Edge 启动器和本地 HTTP API；`internal/archiver/` 为 Go 原生压缩引擎；`cmd/extract/` 为 Linux 解压器。

**已验证**：Go 单元测试，Linux 分卷压缩 / 解压还原，Windows x64 / ARM64 交叉编译；Chromium 对 960×600、960×570、880×570、780×570、740×535 的版式、帮助 / 图表 / 日志切换进行了模拟截图检查，主页面没有整体滚动条及 JS 运行错误。**尚未在 Windows 真机上完成 Edge 应用窗口、系统 DPI、弹窗政策以及原生文件对话框的实测**。尽管新的隔离机制可阻断当前用户 Edge 扩展配置，该实际体验仍需 Windows 验证。
