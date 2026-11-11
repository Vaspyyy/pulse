package gpu

import (
	"slices"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

// NVML's return codes and constants (nvml.h).
const (
	nvmlSuccess          = 0
	nvmlInsufficientSize = 7
	nvmlTemperatureGPU   = 0
	nvmlNotAvailable     = ^uint64(0)
)

// The structs below mirror nvml.h field for field; Go lays them out as C
// does (checked by TestLayout).

type nvmlUtilization struct{ gpu, memory uint32 }

type nvmlMemory struct{ total, free, used uint64 }

type nvmlProcessInfo struct {
	pid               uint32
	usedGpuMemory     uint64
	gpuInstanceID     uint32
	computeInstanceID uint32
}

type nvmlProcessUtilizationSample struct {
	pid       uint32
	timeStamp uint64
	smUtil    uint32
	memUtil   uint32
	encUtil   uint32
	decUtil   uint32
}

type nvmlLib struct {
	init          func() int32
	deviceCount   func(count *uint32) int32
	handleByIndex func(index uint32, device *uintptr) int32
	name          func(device uintptr, name *byte, length uint32) int32
	utilization   func(device uintptr, u *nvmlUtilization) int32
	memory        func(device uintptr, m *nvmlMemory) int32
	temperature   func(device uintptr, sensor uint32, temp *uint32) int32
	graphicsProcs func(device uintptr, count *uint32, infos unsafe.Pointer) int32
	computeProcs  func(device uintptr, count *uint32, infos unsafe.Pointer) int32
	processUtil   func(device uintptr, samples unsafe.Pointer, count *uint32, lastSeen uint64) int32
	// In milliwatts. Not every GPU has them: nil when the library doesn't.
	powerUsage func(device uintptr, power *uint32) int32
	powerLimit func(device uintptr, limit *uint32) int32
}

// loadNVML loads the NVIDIA management library once per process. It
// returns nil where there is none (no NVIDIA driver), or where it won't
// start (no NVIDIA GPU).
var loadNVML = sync.OnceValue(func() *nvmlLib {
	handle, err := purego.Dlopen("libnvidia-ml.so.1", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil
	}
	l := &nvmlLib{}
	ok := true
	bind := func(fn any, symbol string) {
		addr, err := purego.Dlsym(handle, symbol)
		if err != nil {
			ok = false
			return
		}
		purego.RegisterFunc(fn, addr)
	}
	bind(&l.init, "nvmlInit_v2")
	bind(&l.deviceCount, "nvmlDeviceGetCount_v2")
	bind(&l.handleByIndex, "nvmlDeviceGetHandleByIndex_v2")
	bind(&l.name, "nvmlDeviceGetName")
	bind(&l.utilization, "nvmlDeviceGetUtilizationRates")
	bind(&l.memory, "nvmlDeviceGetMemoryInfo")
	bind(&l.temperature, "nvmlDeviceGetTemperature")
	bind(&l.graphicsProcs, "nvmlDeviceGetGraphicsRunningProcesses_v3")
	bind(&l.computeProcs, "nvmlDeviceGetComputeRunningProcesses_v3")
	bind(&l.processUtil, "nvmlDeviceGetProcessUtilization")
	required := ok
	bind(&l.powerUsage, "nvmlDeviceGetPowerUsage")
	bind(&l.powerLimit, "nvmlDeviceGetEnforcedPowerLimit")
	if ok = required; !ok || l.init() != nvmlSuccess {
		return nil
	}
	// No nvmlShutdown: the library lives as long as the process, which
	// for a one-shot command is a moment and for the TUI is the point.
	return l
})

func readNVML() []Device {
	l := loadNVML()
	if l == nil {
		return nil
	}
	var count uint32
	if l.deviceCount(&count) != nvmlSuccess {
		return nil
	}
	var out []Device
	for i := range count {
		var dev uintptr
		if l.handleByIndex(i, &dev) != nvmlSuccess {
			continue
		}
		d := Device{Vendor: "nvidia", Name: "NVIDIA GPU", Util: -1}
		var name [96]byte
		if l.name(dev, &name[0], uint32(len(name))) == nvmlSuccess {
			d.Name = shortName(string(name[:max(slices.Index(name[:], 0), 0)]))
		}
		var u nvmlUtilization
		if l.utilization(dev, &u) == nvmlSuccess {
			d.Util = float64(u.gpu)
		}
		var m nvmlMemory
		if l.memory(dev, &m) == nvmlSuccess {
			d.MemUsed, d.MemTotal = m.used, m.total
		}
		var temp uint32
		if l.temperature(dev, nvmlTemperatureGPU, &temp) == nvmlSuccess {
			d.Temp = float64(temp)
		}
		var mw uint32
		if l.powerUsage != nil && l.powerUsage(dev, &mw) == nvmlSuccess {
			d.Power = float64(mw) / 1000
		}
		if l.powerLimit != nil && l.powerLimit(dev, &mw) == nvmlSuccess {
			d.PowerLimit = float64(mw) / 1000
		}
		d.Procs = l.procsLately(dev)
		out = append(out, d)
	}
	return out
}

// procs merges a device's graphics and compute processes (a process can
// be both, with the same memory in each list) and their utilisation.
// procsEvery is how old a GPU's process list may be. Asking for it is by
// far the slowest part of a reading, and who holds how much VRAM changes
// slowly; a monitor that reads every second asks every third time.
const procsEvery = 2500 * time.Millisecond

var lately struct {
	sync.Mutex
	at    map[uintptr]time.Time
	procs map[uintptr][]Proc
}

// procsLately is procs, from at most procsEvery ago.
func (l *nvmlLib) procsLately(dev uintptr) []Proc {
	lately.Lock()
	defer lately.Unlock()
	if at, ok := lately.at[dev]; ok && time.Since(at) < procsEvery {
		return slices.Clone(lately.procs[dev])
	}
	if lately.at == nil {
		lately.at, lately.procs = map[uintptr]time.Time{}, map[uintptr][]Proc{}
	}
	p := l.procs(dev)
	lately.at[dev], lately.procs[dev] = time.Now(), p
	return slices.Clone(p)
}

func (l *nvmlLib) procs(dev uintptr) []Proc {
	byPID := map[int]*Proc{}
	get := func(pid uint32) *Proc {
		p := byPID[int(pid)]
		if p == nil {
			p = &Proc{PID: int(pid)}
			byPID[int(pid)] = p
		}
		return p
	}
	for _, list := range []func(uintptr, *uint32, unsafe.Pointer) int32{l.graphicsProcs, l.computeProcs} {
		for _, info := range sized(func(count *uint32, buf unsafe.Pointer) int32 { return list(dev, count, buf) }, nvmlProcessInfo{}) {
			p := get(info.pid)
			if info.usedGpuMemory != nvmlNotAvailable {
				p.Mem = max(p.Mem, info.usedGpuMemory)
			}
		}
	}
	// Samples since timestamp 0: everything in the driver's buffer, one
	// entry per process that used the GPU in the last sample period.
	samples := sized(func(count *uint32, buf unsafe.Pointer) int32 { return l.processUtil(dev, buf, count, 0) }, nvmlProcessUtilizationSample{})
	for _, s := range samples {
		if s.smUtil <= 100 {
			get(s.pid).Util = max(get(s.pid).Util, float64(s.smUtil))
		}
	}
	out := make([]Proc, 0, len(byPID))
	for _, p := range byPID {
		out = append(out, *p)
	}
	slices.SortFunc(out, func(a, b Proc) int { return a.PID - b.PID })
	return out
}

// sized calls an NVML list function with room for 64 entries, which is
// nearly always enough and saves the call that only asks how many there
// are: each call is a round trip to the driver. Where it isn't enough,
// NVML says how many it has, and sized tries again with room for those.
func sized[T any](call func(count *uint32, buf unsafe.Pointer) int32, _ T) []T {
	count := uint32(64)
	for range 3 {
		buf := make([]T, count)
		n := count
		switch call(&n, unsafe.Pointer(&buf[0])) {
		case nvmlSuccess:
			return buf[:min(int(n), len(buf))]
		case nvmlInsufficientSize:
			count = max(n+8, count*2)
		default:
			return nil
		}
	}
	return nil
}

// shortName drops the maker from a device name: "NVIDIA GeForce RTX 3070"
// is "RTX 3070".
func shortName(name string) string {
	name = strings.TrimSpace(name)
	for _, prefix := range []string{"NVIDIA ", "GeForce "} {
		name = strings.TrimPrefix(name, prefix)
	}
	if name == "" {
		return "NVIDIA GPU"
	}
	return name
}
