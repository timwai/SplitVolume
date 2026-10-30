//go:build windows

package main

import (
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	comdlg              = syscall.NewLazyDLL("comdlg32.dll")
	shell               = syscall.NewLazyDLL("shell32.dll")
	ole                 = syscall.NewLazyDLL("ole32.dll")
	getOpenFileName     = comdlg.NewProc("GetOpenFileNameW")
	shBrowse            = shell.NewProc("SHBrowseForFolderW")
	shGetPath           = shell.NewProc("SHGetPathFromIDListW")
	coInitialize        = ole.NewProc("CoInitializeEx")
	coUninitialize      = ole.NewProc("CoUninitialize")
	coTaskFree          = ole.NewProc("CoTaskMemFree")
	getForegroundWindow = syscall.NewLazyDLL("user32.dll").NewProc("GetForegroundWindow")
)

type openFileName struct {
	Size                      uint32
	Owner, Instance           uintptr
	Filter, CustomFilter      *uint16
	MaxCustomFilter           uint32
	FilterIndex               uint32
	File                      *uint16
	MaxFile                   uint32
	FileTitle                 *uint16
	MaxFileTitle              uint32
	InitialDir, Title         *uint16
	Flags                     uint32
	FileOffset, FileExtension uint16
	DefaultExt                *uint16
	CustomData, Hook          uintptr
	TemplateName              *uint16
	Reserved                  uintptr
	ReservedFlags, FlagsEx    uint32
}
type browseInfo struct {
	Owner, Root        uintptr
	DisplayName, Title *uint16
	Flags              uint32
	Callback, Param    uintptr
	Image              int32
}

func nativeFileDialog() []string {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	coInitialize.Call(0, 2)
	defer coUninitialize.Call()
	buf := make([]uint16, 32768)
	filter := []uint16{'所', '有', '文', '件', 0, '*', '.', '*', 0, 0}
	title := syscall.StringToUTF16Ptr("选择一个或多个文件")
	owner, _, _ := getForegroundWindow.Call()
	of := openFileName{Size: uint32(unsafe.Sizeof(openFileName{})), Owner: owner, Filter: &filter[0], File: &buf[0], MaxFile: uint32(len(buf)), Title: title, Flags: 0x00080000 | 0x00000200 | 0x00001000 | 0x00000800 | 0x00000008}
	ret, _, _ := getOpenFileName.Call(uintptr(unsafe.Pointer(&of)))
	if ret == 0 {
		return nil
	}
	parts := []string{}
	start := 0
	for i, ch := range buf {
		if ch == 0 {
			if i == start {
				break
			}
			parts = append(parts, syscall.UTF16ToString(buf[start:i]))
			start = i + 1
		}
	}
	if len(parts) <= 1 {
		return parts
	}
	out := make([]string, 0, len(parts)-1)
	for _, p := range parts[1:] {
		out = append(out, filepath.Join(parts[0], p))
	}
	return out
}
func nativeFolderDialog(caption string) string {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	coInitialize.Call(0, 2)
	defer coUninitialize.Call()
	name := make([]uint16, 260)
	owner, _, _ := getForegroundWindow.Call()
	bi := browseInfo{Owner: owner, DisplayName: &name[0], Title: syscall.StringToUTF16Ptr(caption), Flags: 0x0001 | 0x0040}
	pidl, _, _ := shBrowse.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return ""
	}
	defer coTaskFree.Call(pidl)
	path := make([]uint16, 32768)
	ok, _, _ := shGetPath.Call(pidl, uintptr(unsafe.Pointer(&path[0])))
	if ok == 0 {
		return ""
	}
	return syscall.UTF16ToString(path)
}
