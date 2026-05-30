//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/SamLaio/dir2opds/internal/server"
)

const (
	windowClass = "dir2opdsLauncherWindow"

	idFolder = 1001
	idBrowse = 1002
	idPort   = 1003
	idStart  = 1004
	idStop   = 1005
	idOpen   = 1006
	idStatus = 1007

	wmCreate  = 0x0001
	wmDestroy = 0x0002
	wmCommand = 0x0111
	wmClose   = 0x0010

	wsOverlappedWindow = 0x00CF0000
	wsVisible          = 0x10000000
	wsChild            = 0x40000000
	wsTabStop          = 0x00010000
	wsBorder           = 0x00800000

	esAutoHScroll = 0x0080
	bsPushButton  = 0x00000000

	cwUseDefault = -2147483648
	swShow       = 5

	bifReturnOnlyFSDirs = 0x0001
	bifNewDialogStyle   = 0x0040
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")

	procRegisterClassExW     = user32.NewProc("RegisterClassExW")
	procCreateWindowExW      = user32.NewProc("CreateWindowExW")
	procDefWindowProcW       = user32.NewProc("DefWindowProcW")
	procDestroyWindow        = user32.NewProc("DestroyWindow")
	procDispatchMessageW     = user32.NewProc("DispatchMessageW")
	procGetDlgItem           = user32.NewProc("GetDlgItem")
	procGetMessageW          = user32.NewProc("GetMessageW")
	procGetWindowTextW       = user32.NewProc("GetWindowTextW")
	procLoadCursorW          = user32.NewProc("LoadCursorW")
	procMessageBoxW          = user32.NewProc("MessageBoxW")
	procPostQuitMessage      = user32.NewProc("PostQuitMessage")
	procSetWindowTextW       = user32.NewProc("SetWindowTextW")
	procShowWindow           = user32.NewProc("ShowWindow")
	procTranslateMessage     = user32.NewProc("TranslateMessage")
	procUpdateWindow         = user32.NewProc("UpdateWindow")
	procGetModuleHandleW     = kernel32.NewProc("GetModuleHandleW")
	procShellExecuteW        = shell32.NewProc("ShellExecuteW")
	procSHBrowseForFolderW   = shell32.NewProc("SHBrowseForFolderW")
	procSHGetPathFromIDListW = shell32.NewProc("SHGetPathFromIDListW")
	procCoInitialize         = ole32.NewProc("CoInitialize")
	procCoTaskMemFree        = ole32.NewProc("CoTaskMemFree")

	hInstance  uintptr
	mainWindow uintptr

	currentServer *http.Server
	currentURL    string
	wndProcAddr   uintptr
)

type launcherSettings struct {
	BooksFolder string `json:"books_folder"`
}

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type point struct {
	x int32
	y int32
}

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}

type browseInfo struct {
	hwndOwner      uintptr
	pidlRoot       uintptr
	pszDisplayName *uint16
	lpszTitle      *uint16
	ulFlags        uint32
	lpfn           uintptr
	lParam         uintptr
	iImage         int32
}

func main() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	server.ConfigureLogger(false, "json", "")

	hInstance, _, _ = procGetModuleHandleW.Call(0)
	className := utf16Ptr(windowClass)
	cursor, _, _ := procLoadCursorW.Call(0, uintptr(32512))
	wndProcAddr = syscall.NewCallback(wndProc)

	wc := wndClassEx{
		cbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		lpfnWndProc:   wndProcAddr,
		hInstance:     hInstance,
		hCursor:       cursor,
		hbrBackground: uintptr(6),
		lpszClassName: className,
	}
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	mainWindow = createWindow(0, windowClass, "dir2opds Launcher",
		wsOverlappedWindow|wsVisible,
		cwUseDefault, cwUseDefault, 580, 230,
		0, 0)

	procShowWindow.Call(mainWindow, swShow)
	procUpdateWindow.Call(mainWindow)

	var m msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func wndProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmCreate:
		createControls(hwnd)
		return 0
	case wmCommand:
		switch int(wParam & 0xffff) {
		case idBrowse:
			if folder := browseFolder(hwnd); folder != "" {
				setText(control(hwnd, idFolder), folder)
				saveSettings(launcherSettings{BooksFolder: folder})
			}
		case idStart:
			startServer(hwnd)
		case idStop:
			stopServer(hwnd)
		case idOpen:
			openBrowser()
		}
		return 0
	case wmClose:
		saveCurrentSettings(hwnd)
		stopServer(hwnd)
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
	return ret
}

func createControls(hwnd uintptr) {
	createWindow(0, "STATIC", "Books folder:", wsChild|wsVisible, 18, 22, 90, 22, hwnd, 0)
	folder := defaultBooksDir()
	createWindow(0, "EDIT", folder, wsChild|wsVisible|wsBorder|wsTabStop|esAutoHScroll, 110, 20, 340, 24, hwnd, idFolder)
	createWindow(0, "BUTTON", "Browse...", wsChild|wsVisible|wsTabStop|bsPushButton, 460, 19, 86, 26, hwnd, idBrowse)

	createWindow(0, "STATIC", "Port:", wsChild|wsVisible, 18, 62, 90, 22, hwnd, 0)
	createWindow(0, "EDIT", "8080", wsChild|wsVisible|wsBorder|wsTabStop|esAutoHScroll, 110, 60, 90, 24, hwnd, idPort)

	createWindow(0, "BUTTON", "Start", wsChild|wsVisible|wsTabStop|bsPushButton, 110, 105, 90, 30, hwnd, idStart)
	createWindow(0, "BUTTON", "Stop", wsChild|wsVisible|wsTabStop|bsPushButton, 210, 105, 90, 30, hwnd, idStop)
	createWindow(0, "BUTTON", "Open Browser", wsChild|wsVisible|wsTabStop|bsPushButton, 310, 105, 120, 30, hwnd, idOpen)

	createWindow(0, "STATIC", "Stopped", wsChild|wsVisible, 18, 155, 530, 24, hwnd, idStatus)
}

func startServer(hwnd uintptr) {
	if currentServer != nil {
		messageBox(hwnd, "dir2opds is already running.", "dir2opds Launcher")
		return
	}

	folder := getText(control(hwnd, idFolder))
	port := getText(control(hwnd, idPort))
	if folder == "" {
		messageBox(hwnd, "Please choose a books folder.", "dir2opds Launcher")
		return
	}
	if port == "" {
		port = "8080"
	}
	saveSettings(launcherSettings{BooksFolder: folder})

	cfg := server.DefaultConfig()
	cfg.Host = "0.0.0.0"
	cfg.Port = port
	cfg.DirRoot = folder
	cfg.EnableHTML = true
	cfg.EnableGzip = true
	cfg.ExtractMetadata = true

	srv, url, err := server.NewHTTPServer(cfg)
	if err != nil {
		messageBox(hwnd, err.Error(), "Unable to start")
		return
	}

	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		messageBox(hwnd, err.Error(), "Unable to listen")
		return
	}

	currentServer = srv
	currentURL = url
	setText(control(hwnd, idStatus), "Running at "+url)

	go func() {
		err := srv.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			currentServer = nil
			currentURL = ""
		}
	}()

	openBrowser()
}

func stopServer(hwnd uintptr) {
	if currentServer == nil {
		setText(control(hwnd, idStatus), "Stopped")
		return
	}
	_ = currentServer.Shutdown(context.Background())
	currentServer = nil
	currentURL = ""
	setText(control(hwnd, idStatus), "Stopped")
}

func openBrowser() {
	if currentURL == "" {
		return
	}
	procShellExecuteW.Call(0,
		uintptr(unsafe.Pointer(utf16Ptr("open"))),
		uintptr(unsafe.Pointer(utf16Ptr(currentURL))),
		0, 0, swShow)
}

func browseFolder(hwnd uintptr) string {
	procCoInitialize.Call(0)

	display := make([]uint16, syscall.MAX_PATH)
	title := utf16Ptr("Choose your books folder")
	bi := browseInfo{
		hwndOwner:      hwnd,
		pszDisplayName: &display[0],
		lpszTitle:      title,
		ulFlags:        bifReturnOnlyFSDirs | bifNewDialogStyle,
	}

	pidl, _, _ := procSHBrowseForFolderW.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return ""
	}
	defer procCoTaskMemFree.Call(pidl)

	path := make([]uint16, syscall.MAX_PATH)
	ok, _, _ := procSHGetPathFromIDListW.Call(pidl, uintptr(unsafe.Pointer(&path[0])))
	if ok == 0 {
		return ""
	}
	return syscall.UTF16ToString(path)
}

func defaultBooksDir() string {
	if settings, err := loadSettings(); err == nil && settings.BooksFolder != "" {
		return settings.BooksFolder
	}

	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	books := filepath.Join(wd, "books")
	if _, err := os.Stat(books); err == nil {
		return books
	}
	return wd
}

func saveCurrentSettings(hwnd uintptr) {
	folder := getText(control(hwnd, idFolder))
	if folder == "" {
		return
	}
	saveSettings(launcherSettings{BooksFolder: folder})
}

func settingsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, "dir2opds")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "launcher.json"), nil
}

func loadSettings() (launcherSettings, error) {
	path, err := settingsPath()
	if err != nil {
		return launcherSettings{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return launcherSettings{}, err
	}
	var settings launcherSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return launcherSettings{}, err
	}
	return settings, nil
}

func saveSettings(settings launcherSettings) {
	path, err := settingsPath()
	if err != nil {
		return
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

func createWindow(exStyle uint32, className, title string, style uint32, x, y, width, height int32, parent, menu uintptr) uintptr {
	hwnd, _, _ := procCreateWindowExW.Call(
		uintptr(exStyle),
		uintptr(unsafe.Pointer(utf16Ptr(className))),
		uintptr(unsafe.Pointer(utf16Ptr(title))),
		uintptr(style),
		uintptr(uint32(x)), uintptr(uint32(y)), uintptr(uint32(width)), uintptr(uint32(height)),
		parent, menu, hInstance, 0,
	)
	return hwnd
}

func control(hwnd uintptr, id int) uintptr {
	ret, _, _ := procGetDlgItem.Call(hwnd, uintptr(id))
	return ret
}

func getText(hwnd uintptr) string {
	buf := make([]uint16, 4096)
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

func setText(hwnd uintptr, text string) {
	procSetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(utf16Ptr(text))))
}

func messageBox(hwnd uintptr, text, title string) {
	procMessageBoxW.Call(hwnd,
		uintptr(unsafe.Pointer(utf16Ptr(text))),
		uintptr(unsafe.Pointer(utf16Ptr(title))),
		0)
}

func utf16Ptr(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		panic(fmt.Sprintf("invalid utf16 string %q: %v", s, err))
	}
	return p
}
