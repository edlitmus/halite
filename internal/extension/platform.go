package extension

import (
	"bytes"
	"debug/buildinfo"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// ExecutablePlatform answers with the `<goos>/<goarch>` key the manifest
// should file the executable under, read from the executable itself.
//
// It used to be runtime.GOOS/GOARCH: the platform of the machine running
// the bundler, not of the binary it was bundling. Those agree only when
// the author bundles on the platform the extension is for, which is the
// one thing cross-compiling exists to avoid -- so a linux/amd64 binary
// bundled on a Mac went out labelled darwin/arm64, a Linux host refused
// it as carrying no executable for linux/amd64, and a Mac that did find
// its own label failed to start it with `exec format error`. The label
// was a claim about the file that nobody had checked against the file.
//
// Two sources, read independently, and they must agree:
//
//   - The object header -- ELF, Mach-O or PE -- always names the
//     architecture, and names the operating system for Mach-O (darwin)
//     and PE (windows). For ELF it names the operating system only
//     sometimes: see elfOS.
//   - Go's build information, which `go build` writes into every Go
//     binary and which records GOOS and GOARCH as the toolchain saw
//     them. Present in a stripped binary too (`-ldflags=-s -w` drops
//     the symbol table, not .go.buildinfo); absent from anything not
//     built by Go.
//
// A disagreement between them, or between either and -platform, is
// refused rather than resolved, because the only way to resolve it is to
// pick one and the cost of picking wrong is a bundle that every host it
// reaches refuses. Where neither source can say what the operating
// system is, the answer is to require -platform, never to fall back to
// the host -- falling back to the host is the defect.
//
// override is the -platform flag: empty, or `goos/goarch`.
func ExecutablePlatform(path, override string) (string, error) {
	var wantOS, wantArch string
	if override != "" {
		var err error
		if wantOS, wantArch, err = splitPlatform(override); err != nil {
			return "", err
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	head := make([]byte, 2)
	_, err = io.ReadFull(f, head)
	f.Close()
	if err == nil && bytes.Equal(head, []byte("#!")) {
		// A script has no header to read the platform from, and none to
		// contradict a flag with. The bundle still has to file it under
		// some platform -- the loader looks executables up by exactly
		// one -- so the author names it. docs/extensions.md says why a
		// script is not portable everywhere anyway.
		if override == "" {
			return "", fmt.Errorf("%s is a script, which carries no platform of its own: "+
				"name the one it is for with -platform goos/goarch", path)
		}
		return override, nil
	}

	hdr, err := readHeader(path)
	if err != nil {
		return "", fmt.Errorf("cannot tell what platform %s is for: %v; "+
			"halite reads the platform from an ELF, Mach-O or PE header "+
			"and will not guess it", path, err)
	}

	// Go's own record, when there is one. An error here means "not a Go
	// binary" (or one too old to carry it), which is not a reason to
	// refuse: it only means the header is the one source.
	var goOS, goArch string
	if info, err := buildinfo.ReadFile(path); err == nil {
		for _, s := range info.Settings {
			switch s.Key {
			case "GOOS":
				goOS = s.Value
			case "GOARCH":
				goArch = s.Value
			}
		}
	}
	if goArch != "" && goArch != hdr.arch {
		return "", fmt.Errorf("%s says it was built for GOARCH=%s but its %s header is for %s; "+
			"refusing to choose between them", path, goArch, hdr.format, hdr.arch)
	}
	if goOS != "" && hdr.os != "" && goOS != hdr.os {
		return "", fmt.Errorf("%s says it was built for GOOS=%s but its %s header is for %s; "+
			"refusing to choose between them", path, goOS, hdr.format, hdr.os)
	}

	goos := hdr.os
	if goOS != "" {
		goos = goOS
	}

	if override != "" {
		if wantArch != hdr.arch {
			return "", fmt.Errorf("-platform %s does not match %s, whose %s header is for %s",
				override, path, hdr.format, hdr.arch)
		}
		if goos != "" && wantOS != goos {
			return "", fmt.Errorf("-platform %s does not match %s, which is built for %s",
				override, path, Platform(goos, hdr.arch))
		}
		return override, nil
	}
	if goos == "" {
		return "", fmt.Errorf("cannot tell which operating system %s is for: it is not a Go binary, "+
			"and its ELF header (OSABI %d) does not say -- Linux and illumos both write 0 there; "+
			"name it with -platform goos/goarch", path, hdr.osabi)
	}
	return Platform(goos, hdr.arch), nil
}

// splitPlatform checks the -platform flag's shape. Only the shape: the
// names are Go's, and checking them against a list here would be a
// second copy of `go tool dist list` to fall behind it.
func splitPlatform(p string) (goos, goarch string, err error) {
	goos, goarch, ok := strings.Cut(p, "/")
	if !ok || goos == "" || goarch == "" || strings.Contains(goarch, "/") {
		return "", "", fmt.Errorf("-platform %q is not goos/goarch, such as linux/amd64", p)
	}
	return goos, goarch, nil
}

type header struct {
	format string // "ELF", "Mach-O", "PE"
	os     string // GOOS, or "" when the header does not say
	arch   string // GOARCH; never empty when err is nil
	osabi  elf.OSABI
}

func readHeader(path string) (header, error) {
	if f, err := elf.Open(path); err == nil {
		defer f.Close()
		arch, err := elfArch(f)
		if err != nil {
			return header{}, err
		}
		return header{format: "ELF", os: elfOS(f.OSABI), arch: arch, osabi: f.OSABI}, nil
	}
	if f, err := macho.Open(path); err == nil {
		defer f.Close()
		var arch string
		switch f.Cpu {
		case macho.CpuAmd64:
			arch = "amd64"
		case macho.CpuArm64:
			arch = "arm64"
		default:
			return header{}, fmt.Errorf("Mach-O cpu type %v is not one halite maps to a GOARCH", f.Cpu)
		}
		return header{format: "Mach-O", os: "darwin", arch: arch}, nil
	}
	if f, err := macho.OpenFat(path); err == nil {
		// One manifest entry is one platform; a universal file is
		// several, and filing it under any one of them is a claim about
		// the others it does not make.
		f.Close()
		return header{}, errors.New("it is a universal Mach-O file holding several architectures; " +
			"bundle each thin binary under its own platform")
	}
	if f, err := pe.Open(path); err == nil {
		defer f.Close()
		var arch string
		switch f.Machine {
		case pe.IMAGE_FILE_MACHINE_AMD64:
			arch = "amd64"
		case pe.IMAGE_FILE_MACHINE_ARM64:
			arch = "arm64"
		case pe.IMAGE_FILE_MACHINE_I386:
			arch = "386"
		case pe.IMAGE_FILE_MACHINE_ARMNT:
			arch = "arm"
		default:
			return header{}, fmt.Errorf("PE machine type %#x is not one halite maps to a GOARCH", f.Machine)
		}
		return header{format: "PE", os: "windows", arch: arch}, nil
	}
	return header{}, errors.New("it is not an ELF, Mach-O or PE executable")
}

// elfArch maps the ELF machine to a GOARCH. The machine alone is not
// always enough: EM_PPC64 is ppc64 or ppc64le by byte order, and the
// 64-bit names are checked against the class so that, say, an x32
// (32-bit class, EM_X86_64) object is refused rather than called amd64.
func elfArch(f *elf.File) (string, error) {
	is64 := f.Class == elf.ELFCLASS64
	switch {
	case f.Machine == elf.EM_X86_64 && is64:
		return "amd64", nil
	case f.Machine == elf.EM_AARCH64 && is64:
		return "arm64", nil
	case f.Machine == elf.EM_386 && !is64:
		return "386", nil
	case f.Machine == elf.EM_ARM && !is64:
		return "arm", nil
	case f.Machine == elf.EM_RISCV && is64:
		return "riscv64", nil
	case f.Machine == elf.EM_PPC64 && is64 && f.Data == elf.ELFDATA2LSB:
		return "ppc64le", nil
	case f.Machine == elf.EM_PPC64 && is64 && f.Data == elf.ELFDATA2MSB:
		return "ppc64", nil
	case f.Machine == elf.EM_S390 && is64:
		return "s390x", nil
	case f.Machine == elf.EM_LOONGARCH && is64:
		return "loong64", nil
	}
	return "", fmt.Errorf("ELF machine %v (%v) is not one halite maps to a GOARCH", f.Machine, f.Class)
}

// elfOS is what the ELF header alone says about the operating system,
// which is less than it looks.
//
// EI_OSABI 0 is "System V", and it is what Go writes for Linux *and*
// for illumos -- measured with go1.27.1, `go build` for linux/amd64,
// linux/arm64, linux/386, linux/arm, linux/riscv64 and illumos/amd64 all
// wrote 0. So 0 is not Linux, and this does not say it is: for a Go
// binary the build information settles it, and for anything else the
// author has to say.
//
// FreeBSD is the one this can name, and it is the one that matters most
// here. Go writes ELFOSABI_FREEBSD (9) for every freebsd build --
// measured for freebsd/amd64, freebsd/arm64 and freebsd/riscv64 with the
// same toolchain. FreeBSD's image activator is documented to brand an
// ELF image by that byte or by its ABI note, which would make the byte
// something a FreeBSD binary carries for the kernel's sake rather than
// ours -- but that is the documentation, and no FreeBSD kernel was
// asked while writing this. The ABI note (.note.tag, "FreeBSD") that C
// binaries from FreeBSD's own toolchain also carry is not read: a binary
// branded only by its note would come out as "cannot tell", which asks
// for -platform rather than guessing, and no such binary has been seen
// to test a note reader against.
//
// Go also wrote 2 for netbsd and 12 for openbsd, but nothing here
// targets them and they are left to the build information.
func elfOS(osabi elf.OSABI) string {
	if osabi == elf.ELFOSABI_FREEBSD {
		return "freebsd"
	}
	return ""
}
