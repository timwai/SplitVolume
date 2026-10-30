package archiver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

type Config struct {
	Inputs      []string
	OutputDir   string
	ArchiveName string
	VolumeMiB   int64
	Level       int
	Verify      bool
	SevenZip    string
	Format      string // targz(default), 7z
}

type Progress struct {
	Phase   string // compress, verify, done
	Percent int
	Message string
}

type Result struct {
	Files        []string
	TotalSize    int64
	OriginalSize int64 // total regular-file bytes scanned before compression
}

var invalidName = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)
var percentPattern = regexp.MustCompile(`(?:^|\D)(\d{1,3})%`)

func CleanInputs(paths []string) ([]string, error) {
	seen := make(map[string]bool)
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("无法访问 %q: %w", abs, err)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil, fmt.Errorf("暂不支持此文件类型: %s", abs)
		}
		key := strings.ToLower(filepath.Clean(abs))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, abs)
	}
	// Selecting a folder and a descendant of it should not duplicate the data.
	final := make([]string, 0, len(out))
	for _, p := range out {
		covered := false
		for _, other := range out {
			if strings.EqualFold(p, other) {
				continue
			}
			st, err := os.Stat(other)
			if err == nil && st.IsDir() && within(other, p) {
				covered = true
				break
			}
		}
		if !covered {
			final = append(final, p)
		}
	}
	return final, nil
}

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func NormalizedName(name string) (string, error) {
	name = strings.TrimSpace(name)
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".tar.gz") {
		name = name[:len(name)-len(".tar.gz")]
	} else if strings.HasSuffix(lower, ".7z") {
		name = name[:len(name)-3]
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || invalidName.MatchString(name) || strings.HasSuffix(name, ".") {
		return "", errors.New("压缩包名称为空或包含 Windows 不允许的字符")
	}
	// Windows device names cannot be used for files, including with extensions.
	reserved := map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}
	for i := 1; i <= 9; i++ {
		reserved[fmt.Sprintf("COM%d", i)] = true
		reserved[fmt.Sprintf("LPT%d", i)] = true
	}
	if reserved[strings.ToUpper(strings.SplitN(name, ".", 2)[0])] {
		return "", errors.New("压缩包名称不能使用 Windows 保留名称")
	}
	return name, nil
}

func Validate(cfg Config) (Config, error) {
	var err error
	cfg.Inputs, err = CleanInputs(cfg.Inputs)
	if err != nil {
		return cfg, err
	}
	if len(cfg.Inputs) == 0 {
		return cfg, errors.New("请先添加文件或文件夹")
	}
	cfg.ArchiveName, err = NormalizedName(cfg.ArchiveName)
	if err != nil {
		return cfg, err
	}
	if cfg.VolumeMiB < 1 || cfg.VolumeMiB > 1048576 {
		return cfg, errors.New("每卷大小需在 1 至 1048576 MiB 之间")
	}
	if cfg.Level < 0 || cfg.Level > 9 {
		return cfg, errors.New("压缩等级需在 0 至 9 之间")
	}
	if cfg.Format == "" {
		cfg.Format = "targz"
	}
	if cfg.Format != "7z" && cfg.Format != "targz" {
		return cfg, errors.New("不支持的压缩格式，请选择 tar.gz 或 7z")
	}
	if cfg.Format == "7z" && strings.TrimSpace(cfg.SevenZip) == "" {
		return cfg, errors.New("7z 格式需要安装 7-Zip，或将 7zr.exe 放在本程序旁边；Linux 免安装请选 tar.gz")
	}
	if strings.TrimSpace(cfg.OutputDir) == "" {
		return cfg, errors.New("请选择输出目录")
	}
	cfg.OutputDir, err = filepath.Abs(strings.TrimSpace(cfg.OutputDir))
	if err != nil {
		return cfg, fmt.Errorf("输出目录错误: %w", err)
	}
	if cfg.OutputDir == "" {
		return cfg, errors.New("请选择输出目录")
	}
	for _, p := range cfg.Inputs {
		st, e := os.Stat(p)
		if e != nil {
			return cfg, e
		}
		if st.IsDir() && within(p, cfg.OutputDir) {
			return cfg, fmt.Errorf("输出位置不能放在待压缩的文件夹内部：%s（避免压缩文件不断包含自己）", p)
		}
	}
	if err := os.MkdirAll(cfg.OutputDir, 0755); err != nil {
		return cfg, fmt.Errorf("无法创建输出目录: %w", err)
	}
	// Never automatically overwrite a user file or append to an existing volume set.
	base := archiveBase(cfg)
	if _, err := os.Stat(base); err == nil {
		return cfg, fmt.Errorf("目标文件已存在: %s", base)
	} else if !os.IsNotExist(err) {
		return cfg, err
	}
	matches, err := filepath.Glob(base + ".*")
	if err != nil {
		return cfg, err
	}
	if len(matches) > 0 {
		return cfg, fmt.Errorf("同名分卷已存在，避免覆盖：%s", matches[0])
	}
	return cfg, nil
}

func archiveBase(cfg Config) string {
	ext := ".tar.gz"
	if cfg.Format == "7z" {
		ext = ".7z"
	}
	return filepath.Join(cfg.OutputDir, cfg.ArchiveName+ext)
}

func Find7Zip() string {
	// Prefer a portable exe next to our app; then use a system installation.
	me, _ := os.Executable()
	locations := []string{
		filepath.Join(filepath.Dir(me), "7zr.exe"),
		filepath.Join(filepath.Dir(me), "7zz.exe"),
		filepath.Join(filepath.Dir(me), "7z.exe"),
	}
	for _, key := range []string{"ProgramW6432", "ProgramFiles", "ProgramFiles(x86)"} {
		if base := os.Getenv(key); base != "" {
			locations = append(locations, filepath.Join(base, "7-Zip", "7z.exe"))
		}
	}
	for _, p := range locations {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	for _, name := range []string{"7zz.exe", "7z.exe", "7zr.exe", "7zz", "7z", "7zr"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

func cwdAndInputs(paths []string) (string, []string) {
	// Choose the deepest common directory of the parents, so archives usually
	// contain each selected basename rather than complete filesystem paths.
	base := filepath.Dir(paths[0])
	for _, p := range paths[1:] {
		for !within(base, filepath.Dir(p)) {
			parent := filepath.Dir(base)
			if parent == base {
				base = ""
				break
			} // different drives on Windows
			base = parent
		}
		if base == "" {
			break
		}
	}
	if base == "" {
		base = filepath.Dir(paths[0])
	}
	items := make([]string, 0, len(paths))
	for _, p := range paths {
		if rel, err := filepath.Rel(base, p); err == nil && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && rel != ".." {
			items = append(items, rel)
		} else {
			items = append(items, p)
		}
	}
	return base, items
}

func makeListFile(paths []string) (string, error) {
	f, err := os.CreateTemp("", "split7z-input-*.txt")
	if err != nil {
		return "", err
	}
	defer f.Close()
	for _, p := range paths {
		if !utf8.ValidString(p) {
			os.Remove(f.Name())
			return "", errors.New("存在无效 UTF-8 文件名")
		}
		if _, err = io.WriteString(f, p+"\n"); err != nil {
			os.Remove(f.Name())
			return "", err
		}
	}
	return f.Name(), nil
}

func archiveParts(base string) ([]string, int64, error) {
	files, err := filepath.Glob(base + ".[0-9]*")
	if err != nil {
		return nil, 0, err
	}
	sort.Strings(files)
	var size int64
	for _, f := range files {
		if st, e := os.Stat(f); e == nil && st.Mode().IsRegular() {
			size += st.Size()
		}
	}
	return files, size, nil
}

func cleanPartial(base string) {
	files, _, _ := archiveParts(base)
	for _, p := range files {
		_ = os.Remove(p)
	}
}

type streamParser struct {
	mu          sync.Mutex
	pending     []byte
	phase       string
	emit        func(Progress)
	lastPercent int
	logs        int
}

func (s *streamParser) Write(b []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range b {
		if c == '\n' || c == '\r' {
			if len(s.pending) > 0 {
				s.consume(string(s.pending))
				s.pending = s.pending[:0]
			}
		} else if len(s.pending) < 8192 {
			s.pending = append(s.pending, c)
		}
	}
	return len(b), nil
}
func (s *streamParser) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) > 0 {
		s.consume(string(s.pending))
		s.pending = nil
	}
}
func (s *streamParser) consume(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	if all := percentPattern.FindAllStringSubmatch(line, -1); len(all) > 0 {
		v, _ := strconv.Atoi(all[len(all)-1][1])
		if v >= 0 && v <= 100 && v != s.lastPercent {
			s.lastPercent = v
			s.emit(Progress{Phase: s.phase, Percent: v})
		}
		// Percent lines are noisy and can include filenames. Don't flood log.
		if strings.Contains(line, "%") && !strings.Contains(line, "ERROR") {
			return
		}
	}
	if s.logs < 120 && (strings.Contains(strings.ToLower(line), "error") || strings.Contains(strings.ToLower(line), "warning") || strings.HasPrefix(line, "Everything is Ok") || strings.HasPrefix(line, "Files:") || strings.HasPrefix(line, "Size:") || strings.HasPrefix(line, "Compressed:") || strings.HasPrefix(line, "Folders:")) {
		s.logs++
		s.emit(Progress{Phase: s.phase, Percent: s.lastPercent, Message: line})
	}
}

func launch(ctx context.Context, exe, dir string, args []string, phase string, progress func(Progress)) error {
	cmd := exec.CommandContext(ctx, exe, args...)
	prepareExec(cmd)
	cmd.Dir = dir
	reader := &streamParser{emit: progress, phase: phase, lastPercent: -1}
	cmd.Stdout = reader
	cmd.Stderr = reader
	err := cmd.Run()
	reader.flush()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("7-Zip %s 失败: %w", phase, err)
	}
	return nil
}

func Run(ctx context.Context, original Config, progress func(Progress)) (Result, error) {
	if progress == nil {
		progress = func(Progress) {}
	}
	cfg, err := Validate(original)
	if err != nil {
		return Result{}, err
	}
	if cfg.Format == "targz" {
		return runTarGz(ctx, cfg, progress)
	}
	basePath := archiveBase(cfg)
	cwd, relInputs := cwdAndInputs(cfg.Inputs)
	listfile, err := makeListFile(relInputs)
	if err != nil {
		return Result{}, err
	}
	defer os.Remove(listfile)
	progress(Progress{Phase: "compress", Percent: 0, Message: "正在创建分卷压缩包…"})
	bytesPerVolume := cfg.VolumeMiB * 1024 * 1024
	args := []string{"a", "-t7z", fmt.Sprintf("-v%db", bytesPerVolume), fmt.Sprintf("-mx=%d", cfg.Level), "-y", "-bsp1", "-bso1", "-bse1", "-bb0", "-scsUTF-8", basePath, "@" + listfile}
	err = launch(ctx, cfg.SevenZip, cwd, args, "compress", progress)
	if err != nil {
		cleanPartial(basePath)
		return Result{}, err
	}
	parts, size, err := archiveParts(basePath)
	if err != nil {
		return Result{}, err
	}
	if len(parts) == 0 {
		return Result{}, errors.New("7-Zip 已退出，但找不到生成的 .7z.001 分卷")
	}
	if cfg.Verify {
		progress(Progress{Phase: "verify", Percent: 0, Message: "正在校验所有分卷，可能需要一些时间…"})
		err = launch(ctx, cfg.SevenZip, cfg.OutputDir, []string{"t", "-bsp1", "-bso1", "-bse1", "-bb0", parts[0]}, "verify", progress)
		if err != nil {
			return Result{Files: parts, TotalSize: size}, fmt.Errorf("压缩完成，但校验失败（文件已保留）: %w", err)
		}
	}
	progress(Progress{Phase: "done", Percent: 100, Message: fmt.Sprintf("成功生成 %d 个分卷", len(parts))})
	return Result{Files: parts, TotalSize: size}, nil
}
