package archiver

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// splitWriter writes an ordinary gzip-compressed tar byte stream into numbered
// volumes; concatenating all volumes recovers exactly one standard .tar.gz file.
// Each volume is created exclusively and output remains memory-bounded.
type splitWriter struct {
	ctx     context.Context
	base    string
	size    int64
	part    int
	current *os.File
	used    int64
	names   []string
}

func (w *splitWriter) next() error {
	if w.current != nil {
		if err := w.current.Close(); err != nil {
			return err
		}
		w.current = nil
	}
	w.part++
	name := fmt.Sprintf("%s.%03d", w.base, w.part)
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return fmt.Errorf("无法创建分卷 %s: %w", name, err)
	}
	w.names = append(w.names, name)
	w.current = f
	w.used = 0
	return nil
}
func (w *splitWriter) Write(p []byte) (int, error) {
	count := 0
	for len(p) > 0 {
		if err := w.ctx.Err(); err != nil {
			return count, err
		}
		if w.current == nil || w.used == w.size {
			if err := w.next(); err != nil {
				return count, err
			}
		}
		n := int64(len(p))
		if n > w.size-w.used {
			n = w.size - w.used
		}
		written, err := w.current.Write(p[:int(n)])
		count += written
		w.used += int64(written)
		p = p[written:]
		if err != nil {
			return count, err
		}
		if written == 0 {
			return count, io.ErrShortWrite
		}
	}
	return count, nil
}
func (w *splitWriter) Close() error {
	if w.current == nil {
		return nil
	}
	e := w.current.Close()
	w.current = nil
	return e
}
func (w *splitWriter) cleanup() {
	_ = w.Close()
	for _, name := range w.names {
		_ = os.Remove(name)
	}
}

type sourceEntry struct {
	disk    string
	archive string
	info    os.FileInfo
}

func collectEntries(ctx context.Context, inputs []string) ([]sourceEntry, int64, error) {
	entries := make([]sourceEntry, 0, 128)
	var total int64
	used := make(map[string]bool)
	for _, root := range inputs {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		base := filepath.Base(root)
		unique := base
		for n := 2; used[strings.ToLower(unique)]; n++ {
			unique = fmt.Sprintf("%s_%d", base, n)
		}
		used[strings.ToLower(unique)] = true
		err := filepath.WalkDir(root, func(disk string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("不支持符号链接，请先移除或改为普通文件: %s", disk)
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if !info.IsDir() && !info.Mode().IsRegular() {
				return fmt.Errorf("不支持特殊文件: %s", disk)
			}
			rel, err := filepath.Rel(root, disk)
			if err != nil {
				return err
			}
			name := unique
			if rel != "." {
				name = path.Join(unique, filepath.ToSlash(rel))
			}
			entries = append(entries, sourceEntry{disk: disk, archive: name, info: info})
			if info.Mode().IsRegular() {
				total += info.Size()
			}
			return nil
		})
		if err != nil {
			return nil, 0, err
		}
	}
	return entries, total, nil
}

type countedReader struct {
	ctx      context.Context
	r        io.Reader
	counted  *int64
	total    int64
	previous *int
	progress func(Progress)
}

func (r *countedReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	*r.counted += int64(n)
	pct := 100
	if r.total > 0 {
		pct = int(float64(*r.counted) / float64(r.total) * 100)
	}
	if pct > 100 {
		pct = 100
	}
	if pct > *r.previous {
		*r.previous = pct
		r.progress(Progress{Phase: "compress", Percent: pct})
	}
	return n, err
}

// StreamArchive verifies tar structure and the full gzip footer CRC without
// extracting any files. Useful for verifying concatenated split volumes.
func StreamArchive(ctx context.Context, r io.Reader, progress func(int)) error {
	gz, err := gzip.NewReader(&cancelReader{ctx: ctx, r: r})
	if err != nil {
		return fmt.Errorf("Gzip 流无法打开: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	count := 0
	for {
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return fmt.Errorf("Tar 目录损坏: %w", e)
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA && h.Typeflag != tar.TypeDir {
			return fmt.Errorf("不支持的归档条目: %s", h.Name)
		}
		if _, e = io.Copy(io.Discard, &cancelReader{ctx: ctx, r: tr}); e != nil {
			return e
		}
		count++
		if progress != nil && count%25 == 0 {
			progress(count)
		}
	}
	// tar.Reader sees the tar end markers before the gzip footer. Consume to
	// actual gzip EOF so Gzip CRC32 and uncompressed size are validated.
	if _, e := io.Copy(io.Discard, &cancelReader{ctx: ctx, r: gz}); e != nil {
		return fmt.Errorf("Gzip CRC 校验失败: %w", e)
	}
	if count == 0 {
		return errors.New("压缩包中未发现任何条目")
	}
	return nil
}

type cancelReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *cancelReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func runTarGz(ctx context.Context, cfg Config, progress func(Progress)) (Result, error) {
	progress(Progress{Phase: "compress", Percent: 0, Message: "扫描文件列表（tar.gz，无需安装 7-Zip）…"})
	entries, total, err := collectEntries(ctx, cfg.Inputs)
	if err != nil {
		return Result{}, err
	}
	w := &splitWriter{ctx: ctx, base: archiveBase(cfg), size: cfg.VolumeMiB * 1024 * 1024}
	success := false
	defer func() {
		if !success {
			w.cleanup()
		}
	}()
	if err := w.next(); err != nil {
		return Result{}, err
	}
	gz, err := gzip.NewWriterLevel(w, cfg.Level)
	if err != nil {
		return Result{}, err
	}
	gz.Name = ""
	tw := tar.NewWriter(gz)
	written := int64(0)
	last := 0
	progress(Progress{Phase: "compress", Percent: 0, Message: fmt.Sprintf("正在压缩 %d 个文件和文件夹条目", len(entries))})
	for i, ent := range entries {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		hdr, err := tar.FileInfoHeader(ent.info, "")
		if err != nil {
			return Result{}, err
		}
		hdr.Name = ent.archive
		if ent.info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return Result{}, err
		}
		if ent.info.Mode().IsRegular() {
			f, err := os.Open(ent.disk)
			if err != nil {
				return Result{}, err
			}
			cr := &countedReader{ctx: ctx, r: f, counted: &written, total: total, previous: &last, progress: progress}
			n, e := io.CopyN(tw, cr, ent.info.Size())
			_ = f.Close()
			if e != nil {
				return Result{}, fmt.Errorf("读取失败 %s: %w", ent.disk, e)
			}
			if n != ent.info.Size() {
				return Result{}, fmt.Errorf("文件读取大小变化: %s", ent.disk)
			}
		}
		if total == 0 && len(entries) > 0 {
			pct := int(float64(i+1) / float64(len(entries)) * 100)
			if pct > last {
				last = pct
				progress(Progress{Phase: "compress", Percent: pct})
			}
		}
	}
	if err := tw.Close(); err != nil {
		return Result{}, err
	}
	if err := gz.Close(); err != nil {
		return Result{}, err
	}
	if err := w.Close(); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	// Keep numeric creation order; lexical sorting fails after volume 999.
	names := append([]string(nil), w.names...)
	size := int64(0)
	for _, part := range names {
		st, e := os.Stat(part)
		if e != nil {
			return Result{}, e
		}
		size += st.Size()
	}
	success = true // validated complete archive; retained even if verification fails
	result := Result{Files: names, TotalSize: size, OriginalSize: total}
	if cfg.Verify {
		progress(Progress{Phase: "verify", Percent: 0, Message: "正在校验整个 tar.gz 数据流和 CRC32…"})
		streams := make([]io.Reader, 0, len(names))
		opened := make([]*os.File, 0, len(names))
		defer func() {
			for _, f := range opened {
				_ = f.Close()
			}
		}()
		for _, part := range names {
			f, e := os.Open(part)
			if e != nil {
				return result, fmt.Errorf("压缩已完成，但校验无法打开分卷: %w", e)
			}
			opened = append(opened, f)
			streams = append(streams, f)
		}
		e := StreamArchive(ctx, io.MultiReader(streams...), func(count int) {
			progress(Progress{Phase: "verify", Percent: 0, Message: fmt.Sprintf("已校验 %d 个条目", count)})
		})
		if e != nil {
			return result, fmt.Errorf("压缩已完成，但数据完整性校验失败（保留文件）: %w", e)
		}
		progress(Progress{Phase: "verify", Percent: 100, Message: "数据流和 gzip CRC32 校验通过"})
	}
	progress(Progress{Phase: "done", Percent: 100, Message: fmt.Sprintf("成功生成 %d 个 tar.gz 分卷", len(names))})
	return result, nil
}
