package trainer

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

const chunk = 4 << 20

func scanAll(g *Game, v int64) ([]cand, error) {
	regs, err := readMaps(g.PID)
	if err != nil {
		return nil, err
	}
	wide := v > int32Max
	var out []cand
	for _, r := range regs {
		// managed heap, malloc arenas: private anonymous rw memory
		if len(r.Perms) < 2 || r.Perms[0] != 'r' || r.Perms[1] != 'w' || (r.Path != "" && r.Path != "[heap]") {
			continue
		}
		for a := r.Start; a < r.End; a += chunk {
			n := uint64(chunk)
			if a+n > r.End {
				n = r.End - a
			}
			b, _ := g.read(a, int(n))
			if wide {
				for i := 0; i+8 <= len(b); i += 8 {
					if int64(binary.LittleEndian.Uint64(b[i:])) == v {
						out = append(out, cand{addr: a + uint64(i)})
					}
				}
			} else {
				for i := 0; i+4 <= len(b); i += 4 {
					if binary.LittleEndian.Uint32(b[i:]) == uint32(v) {
						out = append(out, cand{addr: a + uint64(i)})
					}
				}
			}
			if len(out) > maxCands {
				return nil, errors.New(fmt.Sprint("over ", maxCands, " matches, use a bigger value"))
			}
		}
	}
	return out, nil
}

func filter(g *Game, in []cand, v int64) []cand {
	var out []cand
	for _, c := range in {
		b, err := g.read(c.addr, 8)
		if err != nil || len(b) < 8 {
			continue
		}
		if v > int32Max {
			if int64(binary.LittleEndian.Uint64(b)) == v {
				out = append(out, c)
			}
		} else if binary.LittleEndian.Uint32(b) == uint32(v) {
			out = append(out, c)
		}
	}
	return out
}

// isItemStack checks the Il2CppClass at klass: name at +0x10, namespace at +0x18.
func (t *Trainer) isItemStack(g *Game, klass uint64) bool {
	if v, ok := t.klass[klass]; ok {
		return v
	}
	str := func(off uint64) string {
		b, err := g.read(klass+off, 8)
		if err != nil || len(b) < 8 {
			return ""
		}
		s, err := g.read(binary.LittleEndian.Uint64(b), 32)
		if len(s) == 0 {
			return ""
		}
		if i := bytes.IndexByte(s, 0); i >= 0 {
			s = s[:i]
		}
		return string(s)
	}
	ok := str(0x10) == "ItemStack" && str(0x18) == "GoF2Remake.Data"
	t.klass[klass] = ok
	return ok
}

// findStacks returns addresses of managed ItemStack objects holding quantity qty (and id wantID if not 0).
// Object layout: klass, monitor, int id (+0x10), int quantity (+0x14).
func (t *Trainer) findStacks(g *Game, qty, wantID uint32) ([]uint64, error) {
	regs, err := readMaps(g.PID)
	if err != nil {
		return nil, err
	}
	var out []uint64
	for _, r := range regs {
		if len(r.Perms) < 2 || r.Perms[0] != 'r' || r.Perms[1] != 'w' || (r.Path != "" && r.Path != "[heap]") {
			continue
		}
		for a := r.Start; a < r.End; a += chunk {
			n := uint64(chunk + 24)
			if a+n > r.End {
				n = r.End - a
			}
			b, _ := g.read(a, int(n))
			for i := 0; i+24 <= len(b) && i < chunk; i += 8 {
				if binary.LittleEndian.Uint32(b[i+20:]) != qty || binary.LittleEndian.Uint64(b[i+8:]) != 0 {
					continue
				}
				if id := binary.LittleEndian.Uint32(b[i+16:]); id == 0 || id > 100000 || (wantID != 0 && id != wantID) {
					continue
				}
				k := binary.LittleEndian.Uint64(b[i:])
				if k > 0x10000 && t.isItemStack(g, k) {
					out = append(out, a+uint64(i))
				}
			}
		}
	}
	return out, nil
}
