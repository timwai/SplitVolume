//go:build windows

// SplitVolume uses a dedicated, extension-free Microsoft Edge app-mode window
// with an ephemeral isolated browser profile; it never reuses the user's Edge
// profile, extensions, or startup tabs. A local server serves the UI. The compression engine
// remains the original Go implementation and requires no external executable
// for tar.gz volumes. No CDN or Internet access is used by the interface.
package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"split7z/internal/archiver"
)

//go:embed web/*
var assets embed.FS

var (
	user32       = syscall.NewLazyDLL("user32.dll")
	messageBox   = user32.NewProc("MessageBoxW")
	getWorkArea  = user32.NewProc("SystemParametersInfoW")
	getSystemDPI = user32.NewProc("GetDpiForSystem")
)

type screenRect struct{ Left, Top, Right, Bottom int32 }

// Compact default window; keep the whole workspace visible without page scrolling.
// On narrow screens, the UI switches to two tabs for files and settings.
func defaultWindowArgs() (string, string) {
	const baseW, baseH = 960, 630
	r := screenRect{}
	ok, _, _ := getWorkArea.Call(0x0030, 0, uintptr(unsafe.Pointer(&r)), 0) // SPI_GETWORKAREA
	if ok == 0 || r.Right <= r.Left || r.Bottom <= r.Top {
		return "--window-size=960,630", "--window-position=45,35"
	}
	dpi := uintptr(96)
	if getSystemDPI.Find() == nil {
		if value, _, _ := getSystemDPI.Call(); value >= 96 && value <= 768 {
			dpi = value
		}
	}
	scale := float64(dpi) / 96.0
	workW := int(float64(r.Right-r.Left) / scale)
	workH := int(float64(r.Bottom-r.Top) / scale)
	w, h := baseW, baseH
	if workW-24 < w {
		w = workW - 24
	}
	if workH-24 < h {
		h = workH - 24
	}
	// Small screens switch to one visible panel at a time rather than scrolling the window.
	if w < 600 {
		w = workW
	}
	if h < 440 {
		h = workH
	}
	if w < 1 || h < 1 {
		return "--window-size=960,630", "--window-position=45,35"
	}
	x := int(float64(r.Left)/scale) + (workW-w)/2
	y := int(float64(r.Top)/scale) + (workH-h)/2
	return fmt.Sprintf("--window-size=%d,%d", w, h), fmt.Sprintf("--window-position=%d,%d", x, y)
}

type entry struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Directory string `json:"directory"`
	Size      int64  `json:"size"`
	Type      string `json:"type"`
}
type volume struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}
type snapshot struct {
	Inputs            []entry  `json:"inputs"`
	Running           bool     `json:"running"`
	Finished          bool     `json:"finished"`
	Successful        bool     `json:"successful"`
	Percent           int      `json:"percent"`
	Status            string   `json:"status"`
	Phase             string   `json:"phase"`
	OriginalSize      int64    `json:"originalSize"`
	TotalSize         int64    `json:"totalSize"`
	Volumes           []volume `json:"volumes"`
	ElapsedSeconds    int64    `json:"elapsedSeconds"`
	Logs              []string `json:"logs"`
	LastError         string   `json:"lastError"`
	OutputDir         string   `json:"outputDir"`
	ArchiveName       string   `json:"archiveName"`
	SevenZipAvailable bool     `json:"sevenZipAvailable"`
}
type apiRequest struct {
	Paths       []string `json:"paths"`
	OutputDir   string   `json:"outputDir"`
	ArchiveName string   `json:"archiveName"`
	VolumeMiB   int64    `json:"volumeMiB"`
	Level       int      `json:"level"`
	Format      string   `json:"format"`
	Verify      bool     `json:"verify"`
}
type application struct {
	sync.Mutex
	entries        []entry
	outputDir      string
	archiveName    string
	partsBase      string
	inputSize      int64
	running        bool
	finished       bool
	successful     bool
	percent        int
	status, phase  string
	startTime      time.Time
	elapsed        time.Duration
	lastResult     *archiver.Result
	cancel         context.CancelFunc
	logs           []string
	lastError      string
	lastSeen       time.Time
	clientSeen     bool
	pageClosedAt   time.Time
	nativeDropOpen atomic.Bool
	token          string
}

func newApp() *application {
	return &application{status: "等待压缩任务", phase: "idle", lastSeen: time.Now()}
}
func (a *application) logf(format string, args ...any) {
	a.logs = append(a.logs, fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...)))
	if len(a.logs) > 300 {
		a.logs = a.logs[len(a.logs)-300:]
	}
}
func sizeAndType(p string, info os.FileInfo) (int64, string) {
	if !info.IsDir() {
		return info.Size(), "文件"
	}
	var total int64
	_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if f, e := d.Info(); e == nil {
				total += f.Size()
			}
		}
		return nil
	})
	return total, "文件夹"
}
func (a *application) add(paths []string) error {
	// Do not hold the application lock while scanning potentially large trees.
	candidates := []entry{}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			continue
		}
		sz, kind := sizeAndType(abs, info)
		candidates = append(candidates, entry{Path: abs, Name: filepath.Base(abs), Directory: filepath.Dir(abs), Size: sz, Type: kind})
	}
	if len(candidates) == 0 && len(paths) > 0 {
		return errors.New("文件或文件夹不存在，或者无法访问")
	}
	a.Lock()
	defer a.Unlock()
	if a.running {
		return errors.New("任务运行期间不能更改文件")
	}
	had := len(a.entries)
	for _, e := range candidates {
		duplicate := false
		for _, old := range a.entries {
			if strings.EqualFold(old.Path, e.Path) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			a.entries = append(a.entries, e)
		}
	}
	if len(a.entries) > had {
		if had == 0 {
			a.outputDir = a.entries[0].Directory
			a.archiveName = a.entries[0].Name
		}
		a.resetResult()
		a.logf("已添加 %d 项。", len(a.entries)-had)
	}
	return nil
}
func (a *application) resetResult() {
	a.partsBase = ""
	a.lastResult = nil
	a.inputSize = 0
	a.percent = 0
	a.phase = "idle"
	a.status = "等待压缩任务"
	a.finished = false
	a.successful = false
	a.elapsed = 0
	a.lastError = ""
}

var digits = regexp.MustCompile(`\.\d+$`)

func snapshotVolumes(base string) ([]volume, int64) {
	if base == "" {
		return []volume{}, 0
	}
	matches, _ := filepath.Glob(base + ".*")
	sort.Slice(matches, func(i, j int) bool { return matches[i] < matches[j] })
	out := make([]volume, 0, len(matches))
	var total int64
	for _, f := range matches {
		if !digits.MatchString(f) {
			continue
		}
		st, err := os.Stat(f)
		if err != nil || st.IsDir() {
			continue
		}
		out = append(out, volume{Name: filepath.Base(f), Size: st.Size()})
		total += st.Size()
	}
	return out, total
}
func (a *application) state() snapshot {
	a.Lock()
	a.lastSeen = time.Now()
	if !a.clientSeen {
		log.Print("界面已成功连接本地压缩服务")
	}
	a.clientSeen = true
	a.pageClosedAt = time.Time{} // reconnect/reload cancels a pending window-close signal
	s := snapshot{Inputs: append([]entry{}, a.entries...), Running: a.running, Finished: a.finished, Successful: a.successful, Percent: a.percent, Status: a.status, Phase: a.phase, OriginalSize: a.inputSize, Logs: append([]string{}, a.logs...), LastError: a.lastError, OutputDir: a.outputDir, ArchiveName: a.archiveName, SevenZipAvailable: archiver.Find7Zip() != ""}
	base := a.partsBase
	if s.OriginalSize == 0 {
		for _, e := range a.entries {
			s.OriginalSize += e.Size
		}
	}
	if a.running {
		s.ElapsedSeconds = int64(time.Since(a.startTime).Seconds())
	} else {
		s.ElapsedSeconds = int64(a.elapsed.Seconds())
	}
	if a.lastResult != nil {
		s.TotalSize = a.lastResult.TotalSize
		s.OriginalSize = a.lastResult.OriginalSize
	}
	a.Unlock()
	volumes, total := snapshotVolumes(base)
	s.Volumes = volumes
	if s.TotalSize == 0 {
		s.TotalSize = total
	}
	return s
}
func (a *application) begin(req apiRequest) error {
	a.Lock()
	defer a.Unlock()
	if a.running {
		return errors.New("已有压缩任务正在运行")
	}
	inputs := make([]string, 0, len(a.entries))
	for _, e := range a.entries {
		inputs = append(inputs, e.Path)
	}
	zip := archiver.Find7Zip()
	cfg, err := archiver.Validate(archiver.Config{Inputs: inputs, OutputDir: req.OutputDir, ArchiveName: req.ArchiveName, VolumeMiB: req.VolumeMiB, Level: req.Level, Format: req.Format, Verify: req.Verify, SevenZip: zip})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.running = true
	a.finished = false
	a.successful = false
	a.percent = 0
	a.phase = "compress"
	a.status = "正在准备压缩…"
	a.elapsed = 0
	a.lastResult = nil
	a.lastError = ""
	a.startTime = time.Now()
	a.outputDir = cfg.OutputDir
	a.archiveName = cfg.ArchiveName
	a.partsBase = filepath.Join(cfg.OutputDir, cfg.ArchiveName+func() string {
		if cfg.Format == "7z" {
			return ".7z"
		}
		return ".tar.gz"
	}())
	a.inputSize = 0
	for _, e := range a.entries {
		a.inputSize += e.Size
	}
	a.logf("任务开始 | %s | 每卷 %d MiB | %s", filepath.Base(a.partsBase), cfg.VolumeMiB, cfg.Format)
	go func() {
		result, e := archiver.Run(ctx, cfg, func(p archiver.Progress) {
			a.Lock()
			a.phase = p.Phase
			a.percent = p.Percent
			if p.Phase == "verify" {
				a.status = fmt.Sprintf("完整性校验：%d%%", p.Percent)
			} else {
				a.status = fmt.Sprintf("正在压缩：%d%%", p.Percent)
			}
			if p.Message != "" {
				a.logf("%s", p.Message)
			}
			a.Unlock()
		})
		a.Lock()
		defer a.Unlock()
		a.running = false
		a.finished = true
		a.cancel = nil
		a.elapsed = time.Since(a.startTime)
		if e != nil {
			a.lastError = e.Error()
			if errors.Is(e, context.Canceled) {
				a.status = "任务已取消"
				a.logf("已取消；未完成的分卷已清理。")
			} else {
				a.status = "压缩失败"
				a.logf("失败：%v", e)
			}
		} else {
			a.successful = true
			a.lastResult = &result
			a.percent = 100
			a.phase = "done"
			a.status = fmt.Sprintf("压缩完成！共生成 %d 个分卷", len(result.Files))
			a.logf("压缩完成：%d 卷，合计 %.2f MiB", len(result.Files), float64(result.TotalSize)/1048576)
			if cfg.Verify {
				a.logf("完整性校验通过。")
			}
		}
	}()
	return nil
}
func (a *application) handleAPI(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Split-Token") != a.token {
		http.Error(w, "unauthorized", http.StatusForbidden)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.URL.Path == "/api/state" && r.Method == "GET" {
		_ = json.NewEncoder(w).Encode(a.state())
		return
	}
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req apiRequest
	if r.ContentLength > 65536 {
		http.Error(w, "request too large", 413)
		return
	}
	if r.Body != nil {
		err := json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&req)
		if err != nil && err != io.EOF {
			http.Error(w, "invalid request", 400)
			return
		}
	}
	var err error
	switch r.URL.Path {
	case "/api/add-files":
		paths := nativeFileDialog()
		if len(paths) > 0 {
			err = a.add(paths)
		}
	case "/api/add-folder":
		path := nativeFolderDialog("选择要压缩的文件夹")
		if path != "" {
			err = a.add([]string{path})
		}
	case "/api/native-drop":
		err = a.openNativeDrop()
	case "/api/window-closed":
		// A pagehide notification is only a hint: reloads can trigger it too.
		// The main loop gives new page requests time to cancel this signal.
		a.Lock()
		a.pageClosedAt = time.Now()
		a.Unlock()
	case "/api/remove":
		a.Lock()
		if a.running {
			err = errors.New("任务运行期间无法移除文件")
		} else {
			selected := map[string]bool{}
			for _, p := range req.Paths {
				selected[strings.ToLower(p)] = true
			}
			kept := a.entries[:0]
			for _, e := range a.entries {
				if !selected[strings.ToLower(e.Path)] {
					kept = append(kept, e)
				}
			}
			a.entries = kept
			a.resetResult()
			a.logf("已移除选中项目。")
		}
		a.Unlock()
	case "/api/clear":
		a.Lock()
		if a.running {
			err = errors.New("任务运行期间无法清空")
		} else {
			a.entries = []entry{}
			a.resetResult()
			a.logf("已清空待压缩项目。")
		}
		a.Unlock()
	case "/api/browse-output":
		p := nativeFolderDialog("选择分卷输出目录")
		if p != "" {
			a.Lock()
			if !a.running {
				a.outputDir = p
			}
			a.Unlock()
		}
	case "/api/start":
		err = a.begin(req)
	case "/api/cancel":
		a.Lock()
		if a.cancel != nil {
			a.cancel()
			a.status = "正在取消…"
			a.logf("正在取消任务…")
		}
		a.Unlock()
	case "/api/open-output":
		a.Lock()
		dir := a.outputDir
		a.Unlock()
		if dir == "" {
			err = errors.New("尚未设置输出目录")
		} else {
			err = exec.Command("explorer.exe", dir).Start()
		}
	case "/api/clear-log":
		a.Lock()
		a.logs = nil
		a.Unlock()
	case "/api/7zip-url":
		err = exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", "https://www.7-zip.org/download.html").Start()
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func edgePath() string {
	if p, e := exec.LookPath("msedge.exe"); e == nil {
		return p
	}
	for _, base := range []string{os.Getenv("PROGRAMFILES(X86)"), os.Getenv("PROGRAMFILES"), os.Getenv("LOCALAPPDATA")} {
		for _, suffix := range []string{`Microsoft\Edge\Application\msedge.exe`, `Microsoft\Edge Beta\Application\msedge.exe`} {
			p := filepath.Join(base, suffix)
			if base != "" {
				if st, e := os.Stat(p); e == nil && !st.IsDir() {
					return p
				}
			}
		}
	}
	return ""
}
func popError(msg string) {
	title, _ := syscall.UTF16PtrFromString("SplitVolume 启动失败")
	body, _ := syscall.UTF16PtrFromString(msg)
	messageBox.Call(0, uintptr(unsafe.Pointer(body)), uintptr(unsafe.Pointer(title)), 0x10)
}

// Diagnostic logs remain local and never contain API tokens or file contents.
// This log is particularly useful because windowsgui executables have no console.
// Check that the loopback listener can serve requests before opening Edge.
// Explicitly disable proxy lookup; corporate HTTP proxies must not intercept
// requests that are meant for the local process.
func preflightLocalService(address string) error {
	client := &http.Client{Timeout: 1200 * time.Millisecond, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	url := "http://" + address + "/healthz"
	var lastErr error
	for attempt := 0; attempt < 12; attempt++ {
		response, err := client.Get(url)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
			err = fmt.Errorf("unexpected status %s", response.Status)
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("本地服务健康检查失败：%w", lastErr)
}

func initDiagnosticLogging() *os.File {
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		root, _ = os.UserCacheDir()
	}
	if root == "" {
		return nil
	}
	directory := filepath.Join(root, "SplitVolume", "logs")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil
	}
	path := filepath.Join(directory, "SplitVolume.log")
	if st, err := os.Stat(path); err == nil && st.Size() > 3<<20 {
		_ = os.Rename(path, path+".old")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil
	}
	log.SetOutput(f)
	return f
}

func main() {
	logFile := initDiagnosticLogging()
	if logFile != nil {
		defer logFile.Close()
	}
	log.Printf("SplitVolume 启动：PID=%d", os.Getpid())
	runtime.GOMAXPROCS(max(2, runtime.NumCPU()))
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		popError(err.Error())
		return
	}
	a := newApp()
	a.token = hex.EncodeToString(tokenBytes)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Printf("本地监听失败：%v", err)
		popError("本地压缩服务启动失败：" + err.Error())
		return
	}
	log.Printf("本地服务监听：%s", listener.Addr())
	defer listener.Close()
	webFiles, _ := fs.Sub(assets, "web")
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("/api/", a.handleAPI)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("key") != a.token {
			http.Error(w, "unauthorized", 403)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; object-src 'none'; frame-ancestors 'none'")
		b, e := fs.ReadFile(webFiles, "index.html")
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		b = []byte(strings.ReplaceAll(string(b), "__SPLIT_TOKEN__", a.token))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != a.token {
			http.Error(w, "unauthorized", 403)
			return
		}
		file := strings.TrimPrefix(r.URL.Path, "/assets/")
		if file != "ui.css" && file != "app.js" && file != "icon.png" {
			http.NotFound(w, r)
			return
		}
		b, e := fs.ReadFile(webFiles, file)
		if e != nil {
			http.NotFound(w, r)
			return
		}
		switch filepath.Ext(file) {
		case ".css":
			w.Header().Set("Content-Type", "text/css")
		case ".js":
			w.Header().Set("Content-Type", "text/javascript")
		case ".png":
			w.Header().Set("Content-Type", "image/png")
		}
		w.Header().Set("Cache-Control", "private, max-age=3600")
		_, _ = w.Write(b)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 6 * time.Second}
	go func() {
		if e := server.Serve(listener); e != nil && !errors.Is(e, http.ErrServerClosed) {
			log.Printf("本地服务意外停止：%v", e)
		}
	}()
	if err := preflightLocalService(listener.Addr().String()); err != nil {
		log.Printf("启动检查失败：%v", err)
		popError(err.Error() + "\n可查看 %LOCALAPPDATA%\\SplitVolume\\logs\\SplitVolume.log")
		return
	}
	log.Print("本地服务健康检查成功")
	edge := edgePath()
	if edge == "" {
		log.Print("未找到 Microsoft Edge")
		popError("需要 Microsoft Edge 以显示原型级桌面界面。请安装 Microsoft Edge，然后重新打开。\n压缩和 Linux 解压组件不需要 Edge。")
		return
	}
	cacheDir, _ := os.UserCacheDir()
	if cacheDir == "" {
		cacheDir = os.TempDir()
	}
	// Never reuse the normal user profile (or the old V4 profile). Existing
	// Edge extensions and session restore pages were the source of unrelated
	// browser windows during startup. Each launch gets a clean, unique profile.
	root := filepath.Join(cacheDir, "SplitVolume")
	if e := os.MkdirAll(root, 0700); e != nil {
		popError("无法建立浏览器缓存目录：" + e.Error())
		return
	}
	profile, e := os.MkdirTemp(root, "ui-profile-*")
	if e != nil {
		popError("无法创建独立的浏览器配置：" + e.Error())
		return
	}
	// The browser process is asked to exit when the UI stops polling. Profile
	// deletion may fail while Edge still has files open; leave no shared state.
	defer os.RemoveAll(profile)
	url := "http://" + listener.Addr().String() + "/?key=" + a.token
	windowSize, windowPosition := defaultWindowArgs()
	cmd := exec.Command(edge,
		"--app="+url, windowSize, windowPosition,
		"--user-data-dir="+profile,
		"--no-first-run", "--no-default-browser-check", "--disable-extensions",
		"--no-proxy-server", // Do not route loopback HTTP traffic through a corporate proxy.
		"--disable-component-extensions-with-background-pages",
		"--disable-sync", "--disable-background-mode", "--disable-notifications",
		"--disable-session-crashed-bubble", "--hide-crash-restore-bubble",
	)
	if e := cmd.Start(); e != nil {
		log.Printf("Edge 启动失败：%v", e)
		popError("无法打开 Microsoft Edge：" + e.Error())
		return
	}
	// Never infer that a window was closed just because HTTP polling stopped.
	// Edge may throttle JavaScript when minimized, the PC may sleep, or the
	// browser may need substantially more than 28s to start. The old 28-second
	// watchdog was shutting the compression service down behind a live window.
	log.Printf("Edge 启动：PID=%d; 端口=%s", cmd.Process.Pid, listener.Addr())
	edgeExit := make(chan time.Time, 1)
	go func() {
		if e := cmd.Wait(); e != nil {
			log.Printf("Edge 进程退出：%v", e)
		} else {
			log.Print("Edge 进程已退出")
		}
		edgeExit <- time.Now()
	}()
	startedAt := time.Now()
	var edgeExitedAt time.Time
	tick := time.NewTicker(4 * time.Second)
	defer tick.Stop()
	for range tick.C {
		select {
		case t := <-edgeExit:
			edgeExitedAt = t
		default:
		}
		a.Lock()
		shouldStop := shouldStopBackend(time.Now(), startedAt, a.lastSeen,
			a.pageClosedAt, edgeExitedAt, a.clientSeen, a.running)
		a.Unlock()
		if shouldStop {
			log.Print("检测到界面关闭，正在停止本地服务")
			break
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
	log.Print("本地服务已停止")
}
