package gpu

import (
	"os"
	"reflect"
	"strconv"
	"testing"
	"testing/fstest"
	"unsafe"
)

// TestLayout checks the NVML structs against nvml.h's sizes and offsets
// on a 64-bit machine.
func TestLayout(t *testing.T) {
	var info nvmlProcessInfo
	var s nvmlProcessUtilizationSample
	checks := []struct {
		name      string
		got, want uintptr
	}{
		{"nvmlProcessInfo size", unsafe.Sizeof(info), 24},
		{"nvmlProcessInfo.usedGpuMemory", unsafe.Offsetof(info.usedGpuMemory), 8},
		{"nvmlProcessInfo.gpuInstanceId", unsafe.Offsetof(info.gpuInstanceID), 16},
		{"nvmlProcessUtilizationSample size", unsafe.Sizeof(s), 32},
		{"nvmlProcessUtilizationSample.timeStamp", unsafe.Offsetof(s.timeStamp), 8},
		{"nvmlProcessUtilizationSample.smUtil", unsafe.Offsetof(s.smUtil), 16},
		{"nvmlMemory size", unsafe.Sizeof(nvmlMemory{}), 24},
		{"nvmlUtilization size", unsafe.Sizeof(nvmlUtilization{}), 8},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s is %d, want %d", c.name, c.got, c.want)
		}
	}
}

func TestSysfs(t *testing.T) {
	f := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	sys := fstest.MapFS{
		// AMD: everything.
		"class/drm/card0/device/vendor":                      f("0x1002\n"),
		"class/drm/card0/device/gpu_busy_percent":            f("37\n"),
		"class/drm/card0/device/mem_info_vram_used":          f("2147483648\n"),
		"class/drm/card0/device/mem_info_vram_total":         f("8589934592\n"),
		"class/drm/card0/device/hwmon/hwmon4/temp1_input":    f("61000\n"),
		"class/drm/card0/device/hwmon/hwmon4/power1_average": f("150000000\n"),
		"class/drm/card0/device/hwmon/hwmon4/power1_cap":     f("200000000\n"),
		"class/drm/card0-DP-1/status":                        f("connected\n"),
		// Intel: its driver has no numbers, so no device.
		"class/drm/card1/device/vendor": f("0x8086\n"),
		// NVIDIA is NVML's, and an unknown vendor is nobody's.
		"class/drm/card2/device/vendor":           f("0x10de\n"),
		"class/drm/card2/device/gpu_busy_percent": f("99\n"),
		"class/drm/card3/device/vendor":           f("0x1234\n"),
	}
	got := readSysfs(sys)
	want := []Device{{Name: "AMD GPU", Vendor: "amd", Util: 37, MemUsed: 2 << 30, MemTotal: 8 << 30, Temp: 61, Power: 150, PowerLimit: 200}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	// Load is the power share where there is one, else the utilisation.
	if l, ok := got[0].Load(); !ok || l != 75 {
		t.Errorf("load %.0f %v, want 75 from 150 of 200 W", l, ok)
	}
	if l, ok := (Device{Util: 40}).Load(); !ok || l != 40 {
		t.Errorf("load without power: %.0f %v, want 40", l, ok)
	}
	if _, ok := (Device{Util: -1}).Load(); ok {
		t.Error("a device with no numbers has a load")
	}
	if got := readSysfs(fstest.MapFS{}); got != nil {
		t.Errorf("no /sys/class/drm: %+v", got)
	}
}

func TestShortName(t *testing.T) {
	for in, want := range map[string]string{
		"NVIDIA GeForce RTX 3070": "RTX 3070",
		"NVIDIA RTX A4000":        "RTX A4000",
		"Tesla T4":                "Tesla T4",
		"":                        "NVIDIA GPU",
	} {
		if got := shortName(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// TestLiveNVML reads the real GPU where the NVIDIA library is installed,
// and checks that the numbers hang together. It only asks the driver for
// numbers, as nvidia-smi does: nothing is drawn or started.
func TestLiveNVML(t *testing.T) {
	if testing.Short() {
		t.Skip("live NVML check skipped with -short")
	}
	devs := readNVML()
	if len(devs) == 0 {
		t.Skip("no NVIDIA GPU or library here")
	}
	for _, d := range devs {
		t.Logf("%s: %.0f%%, %.0f of %.0f W, %d/%d MiB, %.0f°C, %d processes", d.Name, d.Util, d.Power, d.PowerLimit, d.MemUsed>>20, d.MemTotal>>20, d.Temp, len(d.Procs))
		if d.Power < 0 || d.Power > 1000 || d.PowerLimit < 0 || (d.PowerLimit > 0 && d.Power > d.PowerLimit*1.2) {
			t.Errorf("implausible power: %.0f of %.0f W", d.Power, d.PowerLimit)
		}
		if d.Name == "" || d.Util < 0 || d.Util > 100 || d.MemTotal == 0 || d.MemUsed > d.MemTotal || d.Temp < 10 || d.Temp > 120 {
			t.Errorf("implausible reading: %+v", d)
		}
		var sum uint64
		for i, p := range d.Procs {
			sum += p.Mem
			if p.Util < 0 || p.Util > 100 {
				t.Errorf("pid %d: %.0f%% of the GPU", p.PID, p.Util)
			}
			if i > 0 && d.Procs[i-1].PID >= p.PID {
				t.Errorf("processes aren't ascending and unique at pid %d", p.PID)
			}
			if _, err := os.Stat("/proc/" + strconv.Itoa(p.PID)); err != nil && p.Mem > 0 {
				t.Logf("pid %d uses the GPU but isn't in /proc (it exited, or lives in another pid namespace)", p.PID)
			}
		}
		if sum > d.MemTotal {
			t.Errorf("processes use %d MiB together, more than the GPU's %d MiB", sum>>20, d.MemTotal>>20)
		}
	}
}
