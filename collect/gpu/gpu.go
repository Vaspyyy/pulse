// Package gpu reads GPUs: NVIDIA through NVML, loaded at runtime without
// cgo, and AMD and Intel from sysfs. A machine without a source for a GPU
// simply has no reading for it.
package gpu

import (
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// Device is one GPU's reading.
type Device struct {
	Name   string // "RTX 3070"
	Vendor string // nvidia, amd or intel
	// Util is the share of the driver's last sample period in which the
	// GPU did anything, in percent. -1 when unknown. It says little about
	// load: an idle desktop reads 20 to 45 % on a GPU that has clocked
	// itself down (measured 2026-11-11 on an RTX 3070 drawing 30 W).
	Util float64
	// Power is what the GPU draws and PowerLimit the most it may, in
	// watts. 0 when unknown.
	Power, PowerLimit float64
	// MemUsed and MemTotal are VRAM in bytes, 0 when unknown.
	MemUsed, MemTotal uint64
	Temp              float64 // °C, 0 when unknown
	// Procs are the processes using the GPU, ascending by pid. Empty when
	// the source doesn't say (AMD and Intel, until fdinfo is read).
	Procs []Proc
}

// Proc is one process's use of a GPU.
type Proc struct {
	PID  int
	Mem  uint64  // VRAM in bytes
	Util float64 // percent of the GPU, 0 when it used none or it's unknown
}

// Load is how loaded the GPU is, in percent: its power draw against its
// power limit, which holds at any clock speed, or Util where the power
// can't be read. ok is false when neither is known.
func (d Device) Load() (percent float64, ok bool) {
	if d.Power > 0 && d.PowerLimit > 0 {
		return min(d.Power/d.PowerLimit*100, 100), true
	}
	return d.Util, d.Util >= 0
}

// Read reads every GPU there is a source for. sys is /sys.
func Read(sys fs.FS) []Device {
	return append(readNVML(), readSysfs(sys)...)
}

var cardName = regexp.MustCompile(`^card\d+$`)

// readSysfs reads AMD and Intel GPUs from /sys/class/drm. NVIDIA cards
// are left to NVML: their driver publishes nothing here.
func readSysfs(sys fs.FS) []Device {
	entries, err := fs.ReadDir(sys, "class/drm")
	if err != nil {
		return nil
	}
	var out []Device
	for _, e := range entries {
		if !cardName.MatchString(e.Name()) {
			continue
		}
		dev := path.Join("class/drm", e.Name(), "device")
		d := Device{Util: -1}
		switch readString(sys, path.Join(dev, "vendor")) {
		case "0x1002":
			d.Vendor, d.Name = "amd", "AMD GPU"
		case "0x8086":
			d.Vendor, d.Name = "intel", "Intel GPU"
		default:
			continue
		}
		if n, ok := readUint(sys, path.Join(dev, "gpu_busy_percent")); ok {
			d.Util = float64(n)
		}
		d.MemUsed, _ = readUint(sys, path.Join(dev, "mem_info_vram_used"))
		d.MemTotal, _ = readUint(sys, path.Join(dev, "mem_info_vram_total"))
		if mons, err := fs.ReadDir(sys, path.Join(dev, "hwmon")); err == nil {
			for _, m := range mons {
				mon := path.Join(dev, "hwmon", m.Name())
				n, ok := readUint(sys, path.Join(mon, "temp1_input"))
				if !ok {
					continue
				}
				d.Temp = float64(n) / 1000
				// Microwatts.
				if n, ok := readUint(sys, path.Join(mon, "power1_average")); ok {
					d.Power = float64(n) / 1e6
				}
				if n, ok := readUint(sys, path.Join(mon, "power1_cap")); ok {
					d.PowerLimit = float64(n) / 1e6
				}
				break
			}
		}
		// Intel's driver has neither number: nothing to show for it.
		if d.Util < 0 && d.MemTotal == 0 {
			continue
		}
		out = append(out, d)
	}
	return out
}

func readString(fsys fs.FS, name string) string {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readUint(fsys fs.FS, name string) (uint64, bool) {
	n, err := strconv.ParseUint(readString(fsys, name), 10, 64)
	return n, err == nil
}
