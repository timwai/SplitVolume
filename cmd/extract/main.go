// splitvolume-extract is a self-contained Linux-compatible tar.gz volume
// extractor. It needs no tar, gzip, 7z, Python, libc, or package manager.
package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var suffixRe = regexp.MustCompile(`^(.*\.tar\.gz)\.([0-9]{3,})$`)

func volumes(first string) ([]string, error) {
	abs, err := filepath.Abs(first)
	if err != nil {
		return nil, err
	}
	m := suffixRe.FindStringSubmatch(abs)
	if m == nil {
		return nil, errors.New("请选择 .tar.gz.001 第一个分卷")
	}
	index, _ := strconv.Atoi(m[2])
	if index != 1 {
		return nil, errors.New("必须从 .001 分卷开始")
	}
	pattern := m[1] + ".*"
	hits, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	type item struct {
		name string
		i    int
	}
	parts := []item{}
	for _, hit := range hits {
		mm := suffixRe.FindStringSubmatch(hit)
		if mm == nil || mm[1] != m[1] {
			continue
		}
		n, e := strconv.Atoi(mm[2])
		if e != nil {
			return nil, e
		}
		parts = append(parts, item{hit, n})
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].i < parts[j].i })
	if len(parts) == 0 {
		return nil, errors.New("未找到分卷")
	}
	names := make([]string, 0, len(parts))
	for j, p := range parts {
		if p.i != j+1 {
			return nil, fmt.Errorf("缺少第 %03d 个分卷", j+1)
		}
		names = append(names, p.name)
	}
	return names, nil
}

func safeTarget(root, tarName string) (string, error) {
	if tarName == "" || path.IsAbs(tarName) {
		return "", fmt.Errorf("归档路径不是相对路径: %q", tarName)
	}
	clean := path.Clean(tarName)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "\\") || strings.Contains(clean, ":") {
		return "", fmt.Errorf("拒绝不安全的归档路径: %q", tarName)
	}
	target := filepath.Join(root, filepath.FromSlash(clean))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("归档路径超出输出文件夹: %q", tarName)
	}
	// Existing symlinks in any component could redirect the extraction outside root.
	walk := root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if part == "." || part == "" {
			continue
		}
		walk = filepath.Join(walk, part)
		if st, e := os.Lstat(walk); e == nil && st.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("输出路径包含符号链接: %s", walk)
		} else if e != nil && !os.IsNotExist(e) {
			return "", e
		}
	}
	return target, nil
}

func extract(first, destination string) error {
	names, err := volumes(first)
	if err != nil {
		return err
	}
	root, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(root, 0755); err != nil {
		return err
	}
	// Prevent traversal if output directory itself is a symlink.
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	streams := make([]io.Reader, 0, len(names))
	opened := make([]*os.File, 0, len(names))
	defer func() {
		for _, f := range opened {
			_ = f.Close()
		}
	}()
	for _, name := range names {
		f, e := os.Open(name)
		if e != nil {
			return e
		}
		opened = append(opened, f)
		streams = append(streams, f)
	}
	gz, err := gzip.NewReader(io.MultiReader(streams...))
	if err != nil {
		return err
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
			return fmt.Errorf("解压读取失败: %w", e)
		}
		dest, e := safeTarget(root, h.Name)
		if e != nil {
			return e
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if e = os.MkdirAll(dest, 0755); e != nil {
				return e
			}
		case tar.TypeReg, tar.TypeRegA:
			if e = os.MkdirAll(filepath.Dir(dest), 0755); e != nil {
				return e
			}
			f, e := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
			if e != nil {
				return fmt.Errorf("无法写入文件（不会覆盖已存在文件） %s: %w", dest, e)
			}
			_, e = io.Copy(f, tr)
			closeErr := f.Close()
			if e != nil {
				return e
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("不支持或不安全的 tar 条目类型 %d: %s", h.Typeflag, h.Name)
		}
		count++
		if count%50 == 0 {
			fmt.Printf("已解压 %d 个条目\n", count)
		}
	}
	// Trigger CRC and size verification at end of gzip stream.
	if _, err = io.Copy(io.Discard, gz); err != nil {
		return fmt.Errorf("压缩数据 CRC 校验失败: %w", err)
	}
	fmt.Printf("成功：%d 个分卷，%d 个条目，输出目录：%s\n", len(names), count, root)
	return nil
}
func main() {
	if len(os.Args) < 2 || len(os.Args) > 3 {
		fmt.Fprintf(os.Stderr, "用法：%s 归档.tar.gz.001 [输出目录]\n", filepath.Base(os.Args[0]))
		os.Exit(2)
	}
	dest := "."
	if len(os.Args) == 3 {
		dest = os.Args[2]
	}
	if err := extract(os.Args[1], dest); err != nil {
		fmt.Fprintln(os.Stderr, "解压失败：", err)
		os.Exit(1)
	}
}
