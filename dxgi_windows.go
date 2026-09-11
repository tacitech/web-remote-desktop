//go:build windows

package main

import (
	"errors"
	"fmt"
	"image"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Screen capture via DXGI Desktop Duplication.
//
// The GDI path (BitBlt) copies the whole desktop through the CPU every single
// frame — measured at 42.5ms for 2560x1600, which alone caps the stream around
// 12fps and burns most of a core. Desktop Duplication instead hands over the
// frame the compositor already has, in GPU memory, and tells us when nothing
// has changed at all. A still screen then costs nothing.
//
// Windows can refuse it (secure desktop, UAC prompt, resolution change, a
// fullscreen exclusive game, session switch). Every one of those is normal, so
// every failure falls back to GDI rather than breaking the stream.

const (
	dxgiErrorWaitTimeout       = 0x887A0027
	dxgiErrorAccessLost        = 0x887A0026
	dxgiErrorInvalidCall       = 0x887A0001
	dxgiErrorNotCurrentlyAvail = 0x887A0004
	dxgiErrorUnsupported       = 0x887A0004
	dxgiErrorSessionDisconnect = 0x887A0028
	dxgiErrorAccessDenied      = 0x887A002B
	dxgiMapRead                = 1

	// How long to leave duplication alone after it refuses outright. Long
	// enough not to hammer the API, short enough that the stream goes back to
	// the fast path soon after whatever blocked it goes away.
	dupRetryAfter = 5 * time.Second

	d3d11SDKVersion       = 7
	d3d11CreateDeviceBGRA = 0x20
	d3d11UsageStaging     = 3
	d3d11CPUAccessRead    = 0x20000
	dxgiFormatB8G8R8A8    = 87
)

var (
	modD3D11          = syscall.NewLazyDLL("d3d11.dll")
	procD3D11Create   = modD3D11.NewProc("D3D11CreateDevice")
	modDXGI           = syscall.NewLazyDLL("dxgi.dll")
	procCreateFactory = modDXGI.NewProc("CreateDXGIFactory1")
)

type dxgiFactory1Vtbl struct {
	dxgiObjVtbl
	EnumAdapters          uintptr
	MakeWindowAssociation uintptr
	GetWindowAssociation  uintptr
	CreateSwapChain       uintptr
	CreateSoftwareAdapter uintptr
	EnumAdapters1         uintptr
	IsCurrent             uintptr
}

// Minimal COM vtable shapes — only the methods used here are named; the rest
// are placeholders so the indexes line up with the real interfaces.
type comObj struct{ vtbl *comVtbl }
type comVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
}

// IDXGIObject: every DXGI interface inherits these four before its own.
type dxgiObjVtbl struct {
	comVtbl
	SetPrivateData          uintptr
	SetPrivateDataInterface uintptr
	GetPrivateData          uintptr
	GetParent               uintptr
}

// ID3D11DeviceChild: likewise for the D3D11 side.
type d3d11ChildVtbl struct {
	comVtbl
	GetDevice               uintptr
	GetPrivateData          uintptr
	SetPrivateData          uintptr
	SetPrivateDataInterface uintptr
}

func comRelease(p unsafe.Pointer) {
	if p == nil {
		return
	}
	o := (*comObj)(p)
	syscall.SyscallN(o.vtbl.Release, uintptr(p))
}

func comQueryInterface(p unsafe.Pointer, iid *syscall.GUID, out *unsafe.Pointer) error {
	o := (*comObj)(p)
	r, _, _ := syscall.SyscallN(o.vtbl.QueryInterface, uintptr(p), uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(out)))
	if r != 0 {
		return fmt.Errorf("QueryInterface: 0x%x", r)
	}
	return nil
}

func guid(d1 uint32, d2, d3 uint16, d4 [8]byte) *syscall.GUID {
	return &syscall.GUID{Data1: d1, Data2: d2, Data3: d3, Data4: d4}
}

var (
	iidDXGIDevice   = guid(0x54ec77fa, 0x1377, 0x44e6, [8]byte{0x8c, 0x32, 0x88, 0xfd, 0x5f, 0x44, 0xc8, 0x4c})
	iidDXGIOutput1  = guid(0x00cddea8, 0x939b, 0x4b83, [8]byte{0xa3, 0x40, 0xa6, 0x85, 0x22, 0x66, 0x66, 0xcc})
	iidD3D11Texture = guid(0x6f15aaf2, 0xd208, 0x4e89, [8]byte{0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c})
	iidDXGIFactory1 = guid(0x770aae78, 0xf26f, 0x4dba, [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87})
)

// Vtable layouts (method index → offset). Only what's needed.
type dxgiDeviceVtbl struct {
	dxgiObjVtbl
	GetAdapter uintptr
}

type dxgiAdapterVtbl struct {
	dxgiObjVtbl
	EnumOutputs uintptr
}

type dxgiOutput1Vtbl struct {
	dxgiObjVtbl
	GetDesc                  uintptr
	GetDisplayModeList       uintptr
	FindClosestMatchingMode  uintptr
	WaitForVBlank            uintptr
	TakeOwnership            uintptr
	ReleaseOwnership         uintptr
	GetGammaControlCaps      uintptr
	SetGammaControl          uintptr
	GetGammaControl          uintptr
	SetDisplaySurface        uintptr
	GetDisplaySurfaceData    uintptr
	GetFrameStatistics       uintptr
	GetDisplayModeList1      uintptr
	FindClosestMatchingMode1 uintptr
	GetDisplaySurfaceData1   uintptr
	DuplicateOutput          uintptr
}

type outputDuplVtbl struct {
	dxgiObjVtbl
	GetDesc              uintptr
	AcquireNextFrame     uintptr
	GetFrameDirtyRects   uintptr
	GetFrameMoveRects    uintptr
	GetFramePointerShape uintptr
	MapDesktopSurface    uintptr
	UnMapDesktopSurface  uintptr
	ReleaseFrame         uintptr
}

type d3d11DeviceVtbl struct {
	comVtbl
	CreateBuffer             uintptr
	CreateTexture1D          uintptr
	CreateTexture2D          uintptr
	CreateTexture3D          uintptr
	CreateShaderResourceView uintptr
}

type d3d11ContextVtbl struct {
	d3d11ChildVtbl
	VSSetConstantBuffers                      uintptr
	PSSetShaderResources                      uintptr
	PSSetShader                               uintptr
	PSSetSamplers                             uintptr
	VSSetShader                               uintptr
	DrawIndexed                               uintptr
	Draw                                      uintptr
	Map                                       uintptr
	Unmap                                     uintptr
	PSSetConstantBuffers                      uintptr
	IASetInputLayout                          uintptr
	IASetVertexBuffers                        uintptr
	IASetIndexBuffer                          uintptr
	DrawIndexedInstanced                      uintptr
	DrawInstanced                             uintptr
	GSSetConstantBuffers                      uintptr
	GSSetShader                               uintptr
	IASetPrimitiveTopology                    uintptr
	VSSetShaderResources                      uintptr
	VSSetSamplers                             uintptr
	Begin                                     uintptr
	End                                       uintptr
	GetData                                   uintptr
	SetPredication                            uintptr
	GSSetShaderResources                      uintptr
	GSSetSamplers                             uintptr
	OMSetRenderTargets                        uintptr
	OMSetRenderTargetsAndUnorderedAccessViews uintptr
	OMSetBlendState                           uintptr
	OMSetDepthStencilState                    uintptr
	SOSetTargets                              uintptr
	DrawAuto                                  uintptr
	DrawIndexedInstancedIndirect              uintptr
	DrawInstancedIndirect                     uintptr
	Dispatch                                  uintptr
	DispatchIndirect                          uintptr
	RSSetState                                uintptr
	RSSetViewports                            uintptr
	RSSetScissorRects                         uintptr
	CopySubresourceRegion                     uintptr
	CopyResource                              uintptr
}

type dxgiOutputDesc struct {
	DeviceName         [32]uint16
	DesktopCoordinates struct{ Left, Top, Right, Bottom int32 }
	AttachedToDesktop  int32
	Rotation           uint32
	Monitor            uintptr
}

type dxgiOutduplFrameInfo struct {
	LastPresentTime           int64
	LastMouseUpdateTime       int64
	AccumulatedFrames         uint32
	RectsCoalesced            int32
	ProtectedContentMaskedOut int32
	PointerPosition           struct {
		Position struct{ X, Y int32 }
		Visible  int32
	}
	TotalMetadataBufferSize uint32
	PointerShapeBufferSize  uint32
}

type d3d11Texture2DDesc struct {
	Width          uint32
	Height         uint32
	MipLevels      uint32
	ArraySize      uint32
	Format         uint32
	SampleDesc     struct{ Count, Quality uint32 }
	Usage          uint32
	BindFlags      uint32
	CPUAccessFlags uint32
	MiscFlags      uint32
}

type d3d11MappedSubresource struct {
	PData      unsafe.Pointer
	RowPitch   uint32
	DepthPitch uint32
}

// dupCapture holds one display's duplication session.
type dupCapture struct {
	device  unsafe.Pointer
	context unsafe.Pointer
	dupl    unsafe.Pointer
	staging unsafe.Pointer
	w, h    int
	ring    [4]*image.RGBA
	next    int
	last    *image.RGBA
}

// errDupLost marks the failures that mean "start a new duplication session",
// as opposed to "duplication is not usable here".
var errDupLost = errors.New("duplication lost")

var (
	dupMu     sync.Mutex
	dupCache  = map[int]*dupCapture{}
	dupBroken = map[int]time.Time{} // displays where DXGI failed; retry later
)

// captureDisplayFast returns a frame via Desktop Duplication, or an error when
// the caller should use the GDI path instead.
func captureDisplayFast(idx int) (*image.RGBA, bool, error) {
	dupMu.Lock()
	defer dupMu.Unlock()

	// After a failure, don't hammer the API — GDI carries the stream meanwhile.
	if t, bad := dupBroken[idx]; bad && time.Since(t) < dupRetryAfter {
		return nil, false, fmt.Errorf("desktop duplication temporarily unavailable")
	}

	d := dupCache[idx]
	if d == nil {
		nd, err := newDupCapture(idx)
		if err != nil {
			dupBroken[idx] = time.Now()
			return nil, false, err
		}
		dupCache[idx] = nd
		d = nd
	}

	img, changed, err := d.grab()
	if err != nil {
		d.close()
		delete(dupCache, idx)
		// A lost session is routine — a UAC prompt, a resolution change, the
		// screen locking. The fix is simply to duplicate again, so don't sit
		// out the backoff: one GDI frame covers this tick and the next tick
		// rebuilds. Anything else is a real refusal and deserves the wait.
		if !errors.Is(err, errDupLost) {
			dupBroken[idx] = time.Now()
		}
		return nil, false, err
	}
	return img, changed, nil
}

// findOutput locates the DXGI output showing display idx, together with the
// adapter that owns it.
//
// The adapter matters more than it looks. On a hybrid laptop the desktop is
// composited by the integrated GPU, but D3D11CreateDevice with no adapter
// picks the discrete one, whose driver then reports a mirrored copy of the
// same output. Duplicating that copy succeeds and returns the right
// resolution — and never delivers a single frame. So find the output first,
// and build the device on whichever adapter really owns it.
func findOutput(idx int) (adapter, output1 unsafe.Pointer, desc dxgiOutputDesc, err error) {
	var fac unsafe.Pointer
	if r, _, _ := procCreateFactory.Call(uintptr(unsafe.Pointer(iidDXGIFactory1)), uintptr(unsafe.Pointer(&fac))); r != 0 {
		return nil, nil, desc, fmt.Errorf("CreateDXGIFactory1: 0x%x", uint32(r))
	}
	defer comRelease(fac)
	fv := (*struct{ vtbl *dxgiFactory1Vtbl })(fac)

	want := displayBounds(idx)
	for i := 0; ; i++ {
		var ad unsafe.Pointer
		if r, _, _ := syscall.SyscallN(fv.vtbl.EnumAdapters1, uintptr(fac), uintptr(i), uintptr(unsafe.Pointer(&ad))); r != 0 {
			break
		}
		av := (*struct{ vtbl *dxgiAdapterVtbl })(ad)
		for j := 0; ; j++ {
			var out unsafe.Pointer
			if r, _, _ := syscall.SyscallN(av.vtbl.EnumOutputs, uintptr(ad), uintptr(j), uintptr(unsafe.Pointer(&out))); r != 0 {
				break
			}
			var cand unsafe.Pointer
			qerr := comQueryInterface(out, iidDXGIOutput1, &cand)
			comRelease(out)
			if qerr != nil {
				continue
			}
			var cd dxgiOutputDesc
			cv := (*struct{ vtbl *dxgiOutput1Vtbl })(cand)
			if r, _, _ := syscall.SyscallN(cv.vtbl.GetDesc, uintptr(cand), uintptr(unsafe.Pointer(&cd))); r != 0 {
				comRelease(cand)
				continue
			}
			if cd.AttachedToDesktop != 0 &&
				int(cd.DesktopCoordinates.Left) == want.Min.X && int(cd.DesktopCoordinates.Top) == want.Min.Y &&
				int(cd.DesktopCoordinates.Right) == want.Max.X && int(cd.DesktopCoordinates.Bottom) == want.Max.Y {
				return ad, cand, cd, nil
			}
			comRelease(cand)
		}
		comRelease(ad)
	}
	return nil, nil, desc, fmt.Errorf("display %d not found in DXGI", idx)
}

func newDupCapture(idx int) (*dupCapture, error) {
	adapter, output1, desc, err := findOutput(idx)
	if err != nil {
		return nil, err
	}
	defer comRelease(adapter)
	defer comRelease(output1)
	ov := (*struct{ vtbl *dxgiOutput1Vtbl })(output1)

	// DriverType must be UNKNOWN when an explicit adapter is given.
	var device, context unsafe.Pointer
	var featureLevel uint32
	r, _, _ := procD3D11Create.Call(
		uintptr(adapter), 0 /*UNKNOWN*/, 0, uintptr(d3d11CreateDeviceBGRA), 0, 0,
		uintptr(d3d11SDKVersion),
		uintptr(unsafe.Pointer(&device)),
		uintptr(unsafe.Pointer(&featureLevel)),
		uintptr(unsafe.Pointer(&context)),
	)
	if r != 0 || device == nil {
		return nil, fmt.Errorf("D3D11CreateDevice: 0x%x", uint32(r))
	}

	d := &dupCapture{device: device, context: context}
	ok := false
	defer func() {
		if !ok {
			d.close()
		}
	}()

	d.w = int(desc.DesktopCoordinates.Right - desc.DesktopCoordinates.Left)
	d.h = int(desc.DesktopCoordinates.Bottom - desc.DesktopCoordinates.Top)
	if d.w <= 0 || d.h <= 0 {
		return nil, fmt.Errorf("invalid display size")
	}

	var dupl unsafe.Pointer
	if r, _, _ := syscall.SyscallN(ov.vtbl.DuplicateOutput, uintptr(output1), uintptr(device), uintptr(unsafe.Pointer(&dupl))); r != 0 {
		return nil, fmt.Errorf("DuplicateOutput: 0x%x", r)
	}
	d.dupl = dupl

	// A CPU-readable copy target: the duplicated frame lives in GPU memory.
	td := d3d11Texture2DDesc{
		Width: uint32(d.w), Height: uint32(d.h),
		MipLevels: 1, ArraySize: 1, Format: dxgiFormatB8G8R8A8,
		Usage: d3d11UsageStaging, CPUAccessFlags: d3d11CPUAccessRead,
	}
	td.SampleDesc.Count = 1
	dev := (*struct{ vtbl *d3d11DeviceVtbl })(device)
	var staging unsafe.Pointer
	if r, _, _ := syscall.SyscallN(dev.vtbl.CreateTexture2D, uintptr(device), uintptr(unsafe.Pointer(&td)), 0, uintptr(unsafe.Pointer(&staging))); r != 0 {
		return nil, fmt.Errorf("CreateTexture2D: 0x%x", r)
	}
	d.staging = staging
	for i := range d.ring {
		d.ring[i] = image.NewRGBA(image.Rect(0, 0, d.w, d.h))
	}

	// Duplication only reports *changes*, so on a screen that happens to be
	// still when we start, the first acquire returns nothing and there is no
	// picture to hand back. Seed the session with one GDI grab — it is the
	// current desktop by definition — and every later still moment then has a
	// frame to repeat.
	seed, err := captureDisplayGDI(idx)
	if err != nil {
		return nil, fmt.Errorf("no first frame: %w", err)
	}
	d.last = seed

	ok = true
	return d, nil
}

// grab returns the current frame and whether it changed since the last call.
func (d *dupCapture) grab() (*image.RGBA, bool, error) {
	dv := (*struct{ vtbl *outputDuplVtbl })(d.dupl)

	var info dxgiOutduplFrameInfo
	var resource unsafe.Pointer
	// Timeout 0: the caller is already on a frame ticker, so there is no reason
	// to sit and wait here. Either a frame is ready this instant or the screen
	// is still, and a still screen should cost nothing.
	r, _, _ := syscall.SyscallN(dv.vtbl.AcquireNextFrame, uintptr(d.dupl), 0,
		uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&resource)))
	switch uint32(r) {
	case 0:
		// got a frame
	case dxgiErrorWaitTimeout:
		// Nothing changed — this is the case that makes an idle desktop free.
		return d.last, false, nil
	case dxgiErrorAccessLost:
		return nil, false, fmt.Errorf("%w: mat phien duplication", errDupLost)
	default:
		return nil, false, fmt.Errorf("AcquireNextFrame: 0x%x", uint32(r))
	}
	defer syscall.SyscallN(dv.vtbl.ReleaseFrame, uintptr(d.dupl))
	defer comRelease(resource)

	// A moved mouse also wakes duplication, but the cursor is not drawn into
	// the frame — the desktop itself is untouched, so there is nothing to
	// re-encode. AccumulatedFrames counts real desktop updates.
	if info.AccumulatedFrames == 0 && info.LastPresentTime == 0 {
		return d.last, false, nil
	}

	var tex unsafe.Pointer
	if err := comQueryInterface(resource, iidD3D11Texture, &tex); err != nil {
		return nil, false, err
	}
	defer comRelease(tex)

	ctx := (*struct{ vtbl *d3d11ContextVtbl })(d.context)
	syscall.SyscallN(ctx.vtbl.CopyResource, uintptr(d.context), uintptr(d.staging), uintptr(tex))

	var mapped d3d11MappedSubresource
	if r, _, _ := syscall.SyscallN(ctx.vtbl.Map, uintptr(d.context), uintptr(d.staging), 0,
		uintptr(dxgiMapRead), 0, uintptr(unsafe.Pointer(&mapped))); r != 0 {
		return nil, false, fmt.Errorf("Map: 0x%x", uint32(r))
	}
	defer syscall.SyscallN(ctx.vtbl.Unmap, uintptr(d.context), uintptr(d.staging), 0)

	// BGRA (GPU) → RGBA (image.RGBA), row by row because the GPU pitch is
	// usually wider than the visible width.
	pitch := int(mapped.RowPitch)
	base := unsafe.Slice((*byte)(mapped.PData), pitch*d.h)
	img := d.ring[d.next]
	d.next = (d.next + 1) % len(d.ring)
	for y := 0; y < d.h; y++ {
		src := base[y*pitch:]
		row := img.Pix[y*img.Stride:]
		for x := 0; x < d.w*4; x += 4 {
			row[x+0] = src[x+2] // R ← B
			row[x+1] = src[x+1] // G
			row[x+2] = src[x+0] // B ← R
			row[x+3] = 255
		}
	}
	d.last = img
	return img, true, nil
}

func (d *dupCapture) close() {
	comRelease(d.staging)
	comRelease(d.dupl)
	comRelease(d.context)
	comRelease(d.device)
	d.staging, d.dupl, d.context, d.device = nil, nil, nil, nil
}
