package trainer

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const libName = "GameAssembly.so"

type Region struct {
	Start, End uint64
	Perms      string
	Path       string
}

type Game struct {
	PID int
	mem *os.File
}

func findPID() (int, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		f, err := os.Open(fmt.Sprintf("/proc/%d/maps", pid))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		found := false
		for sc.Scan() {
			if strings.HasSuffix(sc.Text(), "/"+libName) {
				found = true
				break
			}
		}
		f.Close()
		if found {
			return pid, nil
		}
	}
	return 0, errors.New("game process not found")
}

func readMaps(pid int) ([]Region, error) {
	f, err := os.Open(fmt.Sprintf("/proc/%d/maps", pid))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Region
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		fl := strings.Fields(sc.Text())
		if len(fl) < 5 {
			continue
		}
		var r Region
		a := strings.SplitN(fl[0], "-", 2)
		r.Start, _ = strconv.ParseUint(a[0], 16, 64)
		r.End, _ = strconv.ParseUint(a[1], 16, 64)
		r.Perms = fl[1]
		if len(fl) >= 6 {
			r.Path = strings.Join(fl[5:], " ")
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

func attach() (*Game, error) {
	pid, err := findPID()
	if err != nil {
		return nil, err
	}
	m, err := os.OpenFile(fmt.Sprintf("/proc/%d/mem", pid), os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open mem: %w", err)
	}
	regs, err := readMaps(pid)
	if err != nil {
		m.Close()
		return nil, err
	}
	var base uint64
	for _, r := range regs {
		if strings.HasSuffix(r.Path, "/"+libName) && (base == 0 || r.Start < base) {
			base = r.Start
		}
	}
	h := make([]byte, 4)
	if _, err := m.ReadAt(h, int64(base)); err != nil || !bytes.Equal(h, []byte("\x7fELF")) {
		m.Close()
		return nil, fmt.Errorf("no ELF header at %#x (err=%v)", base, err)
	}
	return &Game{PID: pid, mem: m}, nil
}

func (g *Game) read(addr uint64, n int) ([]byte, error) {
	b := make([]byte, n)
	k, err := g.mem.ReadAt(b, int64(addr))
	return b[:k], err
}

func (g *Game) write(addr uint64, b []byte) error {
	_, err := g.mem.WriteAt(b, int64(addr))
	return err
}
