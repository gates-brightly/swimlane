package doctor

import (
	"debug/elf"
	"debug/macho"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrScript is returned by Platform for a script (#!), which runs anywhere
// its interpreter does.
var ErrScript = errors.New("a script, not a binary")

// Platform reads an executable's headers (nothing is run) and returns the
// GOOS/GOARCH pairs it can run on: one for ELF and thin Mach-O files, one
// per slice for a universal (fat) Mach-O.
func Platform(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return nil, fmt.Errorf("too short to be a binary")
	}
	if magic[0] == '#' && magic[1] == '!' {
		return nil, ErrScript
	}
	if e, err := elf.NewFile(f); err == nil {
		goos := "linux"
		switch e.OSABI {
		case elf.ELFOSABI_FREEBSD:
			goos = "freebsd"
		case elf.ELFOSABI_NETBSD:
			goos = "netbsd"
		case elf.ELFOSABI_OPENBSD:
			goos = "openbsd"
		}
		return []string{goos + "/" + elfArch(e)}, nil
	}
	if m, err := macho.NewFile(f); err == nil {
		return []string{"darwin/" + machoArch(m.Cpu)}, nil
	}
	if fat, err := macho.NewFatFile(f); err == nil {
		var out []string
		for _, a := range fat.Arches {
			out = append(out, "darwin/"+machoArch(a.Cpu))
		}
		return out, nil
	}
	if magic[0] == 'M' && magic[1] == 'Z' {
		return []string{"windows/unknown"}, nil
	}
	return nil, fmt.Errorf("not an ELF or Mach-O binary")
}

func elfArch(e *elf.File) string {
	switch e.Machine {
	case elf.EM_X86_64:
		return "amd64"
	case elf.EM_AARCH64:
		return "arm64"
	case elf.EM_386:
		return "386"
	case elf.EM_ARM:
		return "arm"
	case elf.EM_RISCV:
		return "riscv64"
	case elf.EM_PPC64:
		if e.ByteOrder.String() == "LittleEndian" {
			return "ppc64le"
		}
		return "ppc64"
	case elf.EM_S390:
		return "s390x"
	}
	return strings.ToLower(strings.TrimPrefix(e.Machine.String(), "EM_"))
}

func machoArch(c macho.Cpu) string {
	switch c {
	case macho.CpuAmd64:
		return "amd64"
	case macho.CpuArm64:
		return "arm64"
	case macho.Cpu386:
		return "386"
	case macho.CpuArm:
		return "arm"
	}
	return strings.ToLower(strings.TrimPrefix(c.String(), "Cpu"))
}

// Runs reports whether a binary for one of platforms runs on host
// (GOOS/GOARCH), and whether only through emulation (Rosetta 2 runs
// darwin/amd64 on Apple silicon).
func Runs(platforms []string, host string) (ok, emulated bool) {
	for _, p := range platforms {
		if p == host {
			return true, false
		}
	}
	for _, p := range platforms {
		if p == "darwin/amd64" && host == "darwin/arm64" {
			return true, true
		}
	}
	return false, false
}

// LookPathAll returns every executable named name in the PATH list, in
// order, the way exec.LookPath searches (an empty entry is the current
// directory). The first one is the one that runs.
func LookPathAll(name, pathList string) []string {
	var out []string
	seen := map[string]bool{}
	for _, dir := range filepath.SplitList(pathList) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, name)
		if seen[p] {
			continue
		}
		st, err := os.Stat(p)
		if err != nil || st.IsDir() || st.Mode()&0o111 == 0 {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// sameFile reports whether two paths are the same file after symlinks.
func sameFile(a, b string) bool {
	sa, errA := os.Stat(a)
	sb, errB := os.Stat(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return os.SameFile(sa, sb)
}
