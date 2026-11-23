package trainer

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// ServeDebug: line protocol on a unix socket for manual investigation.
//
//	find HEX ALIGN | keep HEX | list | dump ADDR N | w ADDR HEX
func (t *Trainer) ServeDebug() {
	path := "/tmp/claude-1000/gof2/dbg.sock"
	os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Logf("debug: %v", err)
		return
	}
	var found []uint64
	var klen int
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		sc := bufio.NewScanner(c)
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) == 0 {
				continue
			}
			t.mu.Lock()
			g := t.game()
			if g == nil {
				fmt.Fprintln(c, "no game")
				t.mu.Unlock()
				continue
			}
			switch f[0] {
			case "find":
				pat, _ := hex.DecodeString(f[1])
				al, _ := strconv.Atoi(f[2])
				klen = len(pat)
				found = found[:0]
				regs, _ := readMaps(g.PID)
				for _, r := range regs {
					if len(r.Perms) < 2 || r.Perms[0] != 'r' || r.Perms[1] != 'w' || (r.Path != "" && r.Path != "[heap]") {
						continue
					}
					for a := r.Start; a < r.End; a += chunk {
						n := uint64(chunk + 16)
						if a+n > r.End {
							n = r.End - a
						}
						b, _ := g.read(a, int(n))
						for i := 0; i+len(pat) <= len(b) && i < chunk; i += al {
							if bytes.Equal(b[i:i+len(pat)], pat) {
								found = append(found, a+uint64(i))
							}
						}
					}
					if len(found) > 2000000 {
						break
					}
				}
				fmt.Fprintln(c, len(found))
			case "stacks":
				q, _ := strconv.ParseUint(f[1], 10, 32)
				var id uint64
				if len(f) > 2 {
					id, _ = strconv.ParseUint(f[2], 10, 32)
				}
				as, _ := t.findStacks(g, uint32(q), uint32(id))
				for _, a := range as {
					b, _ := g.read(a, 48)
					fmt.Fprintf(c, "%#x id=%d qty=%d %x\n", a, t.stackID(g, a), q, b)
				}
				fmt.Fprintln(c, "end")
			case "keep":
				pat, _ := hex.DecodeString(f[1])
				klen = len(pat)
				var out []uint64
				for _, a := range found {
					if b, err := g.read(a, len(pat)); err == nil && bytes.Equal(b, pat) {
						out = append(out, a)
					}
				}
				found = out
				fmt.Fprintln(c, len(found))
			case "list":
				for i, a := range found {
					if i >= 60 {
						break
					}
					b, _ := g.read(a-16, 48+klen)
					fmt.Fprintf(c, "%#x %x\n", a, b)
				}
			case "dump":
				a, _ := strconv.ParseUint(strings.TrimPrefix(f[1], "0x"), 16, 64)
				n, _ := strconv.Atoi(f[2])
				b, err := g.read(a, n)
				fmt.Fprintf(c, "%x %v\n", b, err)
			case "w":
				a, _ := strconv.ParseUint(strings.TrimPrefix(f[1], "0x"), 16, 64)
				b, _ := hex.DecodeString(f[2])
				fmt.Fprintln(c, g.write(a, b))
			}
			t.mu.Unlock()
		}
		c.Close()
	}
}
