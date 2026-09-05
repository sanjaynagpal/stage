// Package shortcut creates (and removes) Windows .lnk shortcut files for
// Start Menu / Desktop entries (docs/REQUIREMENTS.md §16 step 4).
//
// Creating a real .lnk requires COM (IShellLinkW + IPersistFile) — there is
// no simpler Windows API, and no support for it in golang.org/x/sys/windows
// beyond CoInitializeEx/CoUninitialize/GUID, so CoCreateInstance is bound
// manually here via LazyDLL, the same idiom internal/proxydetect already
// uses for winhttp.dll, and the two COM interfaces are called through their
// vtables directly (Go has no built-in COM support).
package shortcut

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Spec describes one shortcut to create.
type Spec struct {
	Path        string // the .lnk file's full path
	TargetPath  string // what it points at, e.g. rigger.exe's path
	Description string
	IconPath    string // optional; "" means no explicit icon
	IconIndex   int32
}

// Create builds a .lnk at spec.Path pointing at spec.TargetPath, via
// IShellLinkW + IPersistFile — the standard COM recipe every
// shortcut-creation tool on Windows uses.
func Create(spec Spec) error {
	if err := os.MkdirAll(filepath.Dir(spec.Path), 0o755); err != nil {
		return fmt.Errorf("shortcut: create %s: %w", filepath.Dir(spec.Path), err)
	}

	if err := coInitialize(); err != nil {
		return fmt.Errorf("shortcut: CoInitializeEx: %w", err)
	}
	defer windows.CoUninitialize()

	raw, err := coCreateInstance(&clsidShellLink, &iidShellLinkW, windows.CLSCTX_INPROC_SERVER)
	if err != nil {
		return err
	}
	link := (*iShellLinkW)(raw)
	defer link.Release()

	if err := link.SetPath(spec.TargetPath); err != nil {
		return err
	}
	if err := link.SetDescription(spec.Description); err != nil {
		return err
	}
	if spec.IconPath != "" {
		if err := link.SetIconLocation(spec.IconPath, spec.IconIndex); err != nil {
			return err
		}
	}

	persistRaw, err := link.QueryInterface(&iidPersistFile)
	if err != nil {
		return err
	}
	persist := (*iPersistFile)(persistRaw)
	defer persist.Release()

	return persist.Save(spec.Path)
}

// Remove deletes the .lnk file (and best-effort its now-possibly-empty
// parent Start Menu folder). No COM needed — a .lnk is just a regular
// file. Missing files are not an error.
func Remove(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("shortcut: remove %s: %w", path, err)
	}
	_ = os.Remove(filepath.Dir(path)) // best-effort; fails silently if not empty
	return nil
}

// --- COM plumbing ---

var (
	clsidShellLink = windows.GUID{Data1: 0x00021401, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidShellLinkW  = windows.GUID{Data1: 0x000214F9, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidPersistFile = windows.GUID{Data1: 0x0000010B, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
)

var (
	modole32             = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance = modole32.NewProc("CoCreateInstance")
)

// coInitialize wraps windows.CoInitializeEx, treating S_FALSE (already
// initialized on this thread, ref count incremented) as success rather
// than an error — only a genuine failure HRESULT should propagate.
func coInitialize() error {
	err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)
	if errno, ok := err.(syscall.Errno); ok && errno == 1 { // S_FALSE
		return nil
	}
	return err
}

// coCreateInstance returns the created object as unsafe.Pointer (not
// uintptr): the syscall writes the pointer's bits directly into &obj, so
// obj is always typed as unsafe.Pointer in Go's view — this avoids ever
// reinterpreting an arbitrary uintptr as a pointer after the fact, which
// `go vet` (rightly) flags as a possible misuse elsewhere.
func coCreateInstance(clsid, iid *windows.GUID, clsctx uint32) (unsafe.Pointer, error) {
	var obj unsafe.Pointer
	hr, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(clsid)),
		0,
		uintptr(clsctx),
		uintptr(unsafe.Pointer(iid)),
		uintptr(unsafe.Pointer(&obj)),
	)
	if hr != 0 {
		return nil, fmt.Errorf("shortcut: CoCreateInstance: HRESULT 0x%08X", uint32(hr))
	}
	return obj, nil
}

func hrToError(op string, hr uintptr) error {
	if hr == 0 {
		return nil
	}
	return fmt.Errorf("shortcut: %s: HRESULT 0x%08X", op, uint32(hr))
}

// --- IShellLinkW ---
// Vtable layout: IUnknown's 3 slots first, then IShellLinkW's own methods,
// in their declared order (shobjidl_core.h) — verified against the GUIDs
// this package uses via `reg query HKCR\Interface\{...}` before trusting.
type iShellLinkWVtbl struct {
	QueryInterface, AddRef, Release                          uintptr
	GetPath, GetIDList, SetIDList, GetDescription             uintptr
	SetDescription, GetWorkingDirectory, SetWorkingDirectory  uintptr
	GetArguments, SetArguments, GetHotkey, SetHotkey          uintptr
	GetShowCmd, SetShowCmd, GetIconLocation, SetIconLocation  uintptr
	SetRelativePath, Resolve, SetPath                         uintptr
}

type iShellLinkW struct {
	vtbl *iShellLinkWVtbl
}

func (o *iShellLinkW) call(proc uintptr, args ...uintptr) uintptr {
	all := append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)
	r, _, _ := syscall.SyscallN(proc, all...)
	return r
}

func (o *iShellLinkW) SetPath(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("shortcut: encode target path: %w", err)
	}
	return hrToError("SetPath", o.call(o.vtbl.SetPath, uintptr(unsafe.Pointer(p))))
}

func (o *iShellLinkW) SetDescription(desc string) error {
	p, err := windows.UTF16PtrFromString(desc)
	if err != nil {
		return fmt.Errorf("shortcut: encode description: %w", err)
	}
	return hrToError("SetDescription", o.call(o.vtbl.SetDescription, uintptr(unsafe.Pointer(p))))
}

func (o *iShellLinkW) SetIconLocation(path string, index int32) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("shortcut: encode icon path: %w", err)
	}
	return hrToError("SetIconLocation", o.call(o.vtbl.SetIconLocation, uintptr(unsafe.Pointer(p)), uintptr(index)))
}

func (o *iShellLinkW) QueryInterface(iid *windows.GUID) (unsafe.Pointer, error) {
	var obj unsafe.Pointer
	hr := o.call(o.vtbl.QueryInterface, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&obj)))
	if err := hrToError("QueryInterface", hr); err != nil {
		return nil, err
	}
	return obj, nil
}

func (o *iShellLinkW) Release() {
	o.call(o.vtbl.Release)
}

// --- IPersistFile ---
type iPersistFileVtbl struct {
	QueryInterface, AddRef, Release                uintptr
	GetClassID                                      uintptr
	IsDirty, Load, Save, SaveCompleted, GetCurFile  uintptr
}

type iPersistFile struct {
	vtbl *iPersistFileVtbl
}

func (o *iPersistFile) call(proc uintptr, args ...uintptr) uintptr {
	all := append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)
	r, _, _ := syscall.SyscallN(proc, all...)
	return r
}

func (o *iPersistFile) Save(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("shortcut: encode save path: %w", err)
	}
	const fRememberTrue = 1
	return hrToError("Save", o.call(o.vtbl.Save, uintptr(unsafe.Pointer(p)), fRememberTrue))
}

func (o *iPersistFile) Release() {
	o.call(o.vtbl.Release)
}
