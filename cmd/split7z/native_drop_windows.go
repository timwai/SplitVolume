//go:build windows

package main

// The embedded Edge UI cannot read the absolute paths of items dropped on a
// web page. A small native Win32 drop target receives CF_HDROP/WM_DROPFILES
// from Explorer instead. We pass only the source paths to the archiver: no
// file contents are uploaded to localhost or copied to the temp directory.

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

const (
	dropWMClose            = 0x0010
	dropWMDestroy          = 0x0002
	dropWMPaint            = 0x000F
	dropWMFiles            = 0x0233
	dropWSOverlappedWindow = 0x00CF0000
	dropWSExTopmost        = 0x00000008
	dropSWShow             = 5
)

type dropRect struct{ left, top, right, bottom int32 }
type dropPaint struct {
	hdc                uintptr
	erase              int32
	rect               dropRect
	restore, incUpdate int32
	reserved           [32]byte
}
type dropWndClass struct {
	style                              uint32
	proc                               uintptr
	clsExtra, wndExtra                 int32
	instance, icon, cursor, background uintptr
	menu, name                         *uint16
}

var (
	dropUser32           = syscall.NewLazyDLL("user32.dll")
	dropShell32          = syscall.NewLazyDLL("shell32.dll")
	dropGdi32            = syscall.NewLazyDLL("gdi32.dll")
	dropKernel32         = syscall.NewLazyDLL("kernel32.dll")
	dropRegisterClass    = dropUser32.NewProc("RegisterClassW")
	dropCreateWindow     = dropUser32.NewProc("CreateWindowExW")
	dropGetWindowRect    = dropUser32.NewProc("GetWindowRect")
	dropDestroyWindow    = dropUser32.NewProc("DestroyWindow")
	dropShowWindow       = dropUser32.NewProc("ShowWindow")
	dropUpdateWindow     = dropUser32.NewProc("UpdateWindow")
	dropGetMessage       = dropUser32.NewProc("GetMessageW")
	dropTranslateMessage = dropUser32.NewProc("TranslateMessage")
	dropDispatchMessage  = dropUser32.NewProc("DispatchMessageW")
	dropDefaultProc      = dropUser32.NewProc("DefWindowProcW")
	dropPostQuit         = dropUser32.NewProc("PostQuitMessage")
	dropBeginPaint       = dropUser32.NewProc("BeginPaint")
	dropEndPaint         = dropUser32.NewProc("EndPaint")
	dropDrawText         = dropUser32.NewProc("DrawTextW")
	dropDragAccept       = dropShell32.NewProc("DragAcceptFiles")
	dropQueryFile        = dropShell32.NewProc("DragQueryFileW")
	dropFinish           = dropShell32.NewProc("DragFinish")
	dropGetModule        = dropKernel32.NewProc("GetModuleHandleW")
	dropSetBkMode        = dropGdi32.NewProc("SetBkMode")
	dropSetTextColor     = dropGdi32.NewProc("SetTextColor")
	dropWindowInstances  sync.Map // HWND -> *application
	dropClassOnce        sync.Once
	dropClassErr         error
	dropClassName        = syscall.StringToUTF16Ptr("SplitVolumeNativeDropZone")
	dropWindowCallback   = syscall.NewCallback(dropWindowProc)
)

func drawDropLine(hdc uintptr, text string, rect dropRect, color uintptr) {
	s, _ := syscall.UTF16PtrFromString(text)
	dropSetTextColor.Call(hdc, color)
	dropDrawText.Call(hdc, uintptr(unsafe.Pointer(s)), ^uintptr(0), uintptr(unsafe.Pointer(&rect)), 0x1|0x4|0x20)
}

func dropWindowProc(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	switch msg {
	case dropWMFiles:
		// DragQueryFileW(index=0xFFFFFFFF) reports the number of real paths.
		n, _, _ := dropQueryFile.Call(wp, ^uintptr(0), 0, 0)
		paths := make([]string, 0, min(int(n), 4096))
		for i := uintptr(0); i < n && i < 4096; i++ {
			length, _, _ := dropQueryFile.Call(wp, i, 0, 0)
			if length == 0 || length > 32767 {
				continue
			}
			buf := make([]uint16, length+1)
			got, _, _ := dropQueryFile.Call(wp, i, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
			if got != 0 {
				paths = append(paths, syscall.UTF16ToString(buf[:got]))
			}
		}
		dropFinish.Call(wp)
		if raw, ok := dropWindowInstances.Load(hwnd); ok {
			app := raw.(*application)
			// Large directories are scanned outside the Win32 message loop.
			go func() {
				if len(paths) == 0 {
					return
				}
				if err := app.add(paths); err != nil {
					app.Lock()
					app.lastError = err.Error()
					app.logf("原生拖放导入失败：%v", err)
					app.Unlock()
				}
			}()
		}
		dropDestroyWindow.Call(hwnd)
		return 0
	case dropWMPaint:
		var ps dropPaint
		hdc, _, _ := dropBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		if hdc != 0 {
			dropSetBkMode.Call(hdc, 1) // TRANSPARENT
			drawDropLine(hdc, "将文件或文件夹拖放到此窗口", dropRect{15, 43, 425, 100}, 0x00f17f23)
			drawDropLine(hdc, "直接读取原始路径，不复制到临时目录", dropRect{15, 105, 425, 147}, 0x00555555)
			drawDropLine(hdc, "释放鼠标后自动添加 · 关闭窗口可取消", dropRect{15, 155, 425, 190}, 0x00808080)
			dropEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		}
		return 0
	case dropWMClose:
		dropDestroyWindow.Call(hwnd)
		return 0
	case dropWMDestroy:
		dropWindowInstances.Delete(hwnd)
		dropPostQuit.Call(0)
		return 0
	}
	ret, _, _ := dropDefaultProc.Call(hwnd, uintptr(msg), wp, lp)
	return ret
}

func registerDropWindowClass() error {
	dropClassOnce.Do(func() {
		mod, _, _ := dropGetModule.Call(0)
		wc := dropWndClass{proc: dropWindowCallback, instance: mod, background: 6, name: dropClassName}
		atom, _, e := dropRegisterClass.Call(uintptr(unsafe.Pointer(&wc)))
		if atom == 0 {
			dropClassErr = fmt.Errorf("注册拖放窗口失败：%w", e)
		}
	})
	return dropClassErr
}

func runNativeDropWindow(app *application) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := registerDropWindowClass(); err != nil {
		return err
	}
	title := syscall.StringToUTF16Ptr("SplitVolume · 原生拖放导入")
	// Center near the foreground Explorer/app window instead of pinning the
	// target to the primary monitor; this also works on multi-monitor setups.
	x, y := int32(250), int32(180)
	if foreground, _, _ := getForegroundWindow.Call(); foreground != 0 {
		r := screenRect{}
		if ok, _, _ := dropGetWindowRect.Call(foreground, uintptr(unsafe.Pointer(&r))); ok != 0 && r.Right > r.Left && r.Bottom > r.Top {
			x, y = r.Left+(r.Right-r.Left-450)/2, r.Top+(r.Bottom-r.Top-255)/2
		}
	}
	module, _, _ := dropGetModule.Call(0)
	hwnd, _, e := dropCreateWindow.Call(
		dropWSExTopmost, uintptr(unsafe.Pointer(dropClassName)), uintptr(unsafe.Pointer(title)),
		dropWSOverlappedWindow, uintptr(x), uintptr(y), 450, 255, 0, 0, module, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("打开原生拖放窗口失败：%w", e)
	}
	dropWindowInstances.Store(hwnd, app)
	dropDragAccept.Call(hwnd, 1)
	dropShowWindow.Call(hwnd, dropSWShow)
	dropUpdateWindow.Call(hwnd)
	var msg [7]uintptr // MSG: hwnd, message/padding, wparam, lparam, time/pt/lPrivate
	for {
		n, _, err := dropGetMessage.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0)
		if int32(n) == -1 {
			return fmt.Errorf("原生拖放消息循环失败：%w", err)
		}
		if n == 0 {
			break
		}
		dropTranslateMessage.Call(uintptr(unsafe.Pointer(&msg[0])))
		dropDispatchMessage.Call(uintptr(unsafe.Pointer(&msg[0])))
	}
	return nil
}

func (a *application) openNativeDrop() error {
	a.Lock()
	running := a.running
	a.Unlock()
	if running {
		return errors.New("压缩期间不能添加文件")
	}
	if !a.nativeDropOpen.CompareAndSwap(false, true) {
		return nil
	}
	go func() {
		defer a.nativeDropOpen.Store(false)
		if err := runNativeDropWindow(a); err != nil {
			a.Lock()
			a.lastError = err.Error()
			a.logf("%v", err)
			a.Unlock()
		}
	}()
	return nil
}
