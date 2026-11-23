package trainer

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"
)

const (
	MoneyTarget = int64(9999999999)
	int32Max    = int64(2147483647)
	maxCands    = 4_000_000
)

type cand struct {
	addr uint64
	w    int // 4 or 8, decided when the lock is armed
}

type Trainer struct {
	mu        sync.Mutex
	g         *Game
	quit      chan struct{}
	log       *os.File
	cands     []cand
	scaned    bool
	lock      bool
	klass     map[uint64]bool
	stackPend []uint64
	stackSel  uint64
	stackAlso []uint64
	pendQty   int64
	pendIDs   []uint32
	pendID    uint32
}

func New() *Trainer {
	lf, _ := os.OpenFile(os.Getenv("HOME")+"/.cache/gof2remake-trainer.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	return &Trainer{quit: make(chan struct{}), log: lf, klass: map[uint64]bool{}}
}

func (t *Trainer) Logf(f string, a ...any) {
	if t.log != nil {
		fmt.Fprintf(t.log, time.Now().Format("15:04:05 ")+f+"\n", a...)
	}
}

func (t *Trainer) Stop() {
	close(t.quit)
	t.mu.Lock()
	if t.g != nil {
		t.g.mem.Close()
		t.g = nil
	}
	t.mu.Unlock()
}

// caller holds t.mu
func (t *Trainer) game() *Game {
	if t.g != nil {
		return t.g
	}
	g, err := attach()
	if err != nil {
		return nil
	}
	t.g = g
	t.Logf("attached pid=%d", g.PID)
	return g
}

func (t *Trainer) Run() {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-t.quit:
			return
		case <-tick.C:
		}
		t.mu.Lock()
		t.game()
		if t.lock && t.g != nil {
			for _, c := range t.cands {
				t.writeMoney(c)
			}
		}
		t.mu.Unlock()
	}
}

func (t *Trainer) writeMoney(c cand) error {
	var b [8]byte
	if c.w == 8 {
		binary.LittleEndian.PutUint64(b[:], uint64(MoneyTarget))
		return t.g.write(c.addr, b[:])
	}
	binary.LittleEndian.PutUint32(b[:4], uint32(int32Max))
	return t.g.write(c.addr, b[:4])
}

func (t *Trainer) Candidates() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.cands)
}

func (t *Trainer) Locked() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lock
}

// Reset drops the candidate list and the lock.
func (t *Trainer) Reset() {
	t.mu.Lock()
	t.cands, t.scaned, t.lock = nil, false, false
	t.mu.Unlock()
}

// Search is the first scan on the first call, a narrowing filter on later calls.
func (t *Trainer) Search(v int64) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	g := t.game()
	if g == nil {
		return 0, errors.New("game not attached")
	}
	t.lock = false
	if !t.scaned {
		c, err := scanAll(g, v)
		if err != nil {
			return 0, err
		}
		t.cands, t.scaned = c, true
	} else {
		t.cands = filter(g, t.cands, v)
	}
	t.Logf("search %d -> %d candidates", v, len(t.cands))
	return len(t.cands), nil
}

// SetMoney arms the freeze loop. Width per candidate: 8 bytes when it is 8-aligned and
// the next dword is zero, otherwise 4 bytes clamped to the int32 maximum.
func (t *Trainer) SetMoney(on bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !on {
		t.lock = false
		return nil
	}
	g := t.game()
	if g == nil {
		return errors.New("game not attached")
	}
	if len(t.cands) == 0 {
		return errors.New("find the credits address first")
	}
	if len(t.cands) > 16 {
		return fmt.Errorf("%d candidates, narrow below 17", len(t.cands))
	}
	for i := range t.cands {
		c := &t.cands[i]
		b, err := g.read(c.addr, 8)
		if err != nil || len(b) < 8 {
			return fmt.Errorf("read %#x: %v", c.addr, err)
		}
		c.w = 4
		if c.addr%8 == 0 && binary.LittleEndian.Uint32(b[4:]) == 0 {
			c.w = 8
		}
		t.Logf("lock %#x width %d", c.addr, c.w)
		if err := t.writeMoney(*c); err != nil {
			return err
		}
	}
	t.lock = true
	return nil
}

var creditsRe = regexp.MustCompile(`"credits"\s*:\s*(\d+)`)

// Auto finds the credits in memory using the value in the newest save file, then locks it.
func (t *Trainer) Auto() error {
	files, _ := filepath.Glob(os.Getenv("HOME") + "/.config/unity3d/*/Galaxy on Fire 2/Saves/slot_*.json")
	var newest string
	var nt time.Time
	for _, f := range files {
		if st, err := os.Stat(f); err == nil && st.ModTime().After(nt) {
			newest, nt = f, st.ModTime()
		}
	}
	if newest == "" {
		return errors.New("no save found")
	}
	b, _ := os.ReadFile(newest)
	m := creditsRe.FindSubmatch(b)
	if m == nil {
		return errors.New("no credits in save")
	}
	v, _ := strconv.ParseInt(string(m[1]), 10, 64)
	t.Reset()
	n, err := t.Search(v)
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("credits not in memory: load the save, do not spend, press 1")
	}
	return t.SetMoney(true)
}

func (t *Trainer) stackQty(g *Game, a uint64) (int64, bool) {
	b, err := g.read(a+0x14, 4)
	if err != nil || len(b) < 4 {
		return 0, false
	}
	return int64(binary.LittleEndian.Uint32(b)), true
}

func (t *Trainer) stackID(g *Game, a uint64) uint32 {
	b, err := g.read(a+0x10, 4)
	if err != nil || len(b) < 4 {
		return 0
	}
	return binary.LittleEndian.Uint32(b)
}

// FindStack looks for the ItemStack holding qty. Unique: it becomes the selection and
// pending is 0. Several: they are kept as candidates (pending = count), then SellStep and
// BuyBackStep identify the wanted one.
func (t *Trainer) FindStack(qty int64) (pending int, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	g := t.game()
	if g == nil {
		return 0, errors.New("game not attached")
	}
	if qty < 1 || qty > int32Max {
		return 0, errors.New("bad amount")
	}
	addrs, err := t.findStacks(g, uint32(qty), 0)
	if err != nil {
		return 0, err
	}
	if len(addrs) == 0 {
		return 0, errors.New("no item with that amount")
	}
	t.stackPend, t.pendQty, t.pendIDs = nil, qty, nil
	if len(addrs) == 1 {
		t.stackSel, t.stackAlso = addrs[0], nil
		return 0, nil
	}
	t.stackPend = addrs
	for _, a := range addrs {
		t.pendIDs = append(t.pendIDs, t.stackID(g, a))
	}
	return len(addrs), nil
}

// SellStep runs after the user sold 1 of the wanted item. Candidates that changed are kept; when
// they all share one item id the stack is found (found=true), otherwise sell/buy again.
func (t *Trainer) SellStep() (found bool, left int, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	g := t.game()
	if g == nil || len(t.stackPend) == 0 {
		return false, 0, errors.New("start again")
	}
	var ch []uint64
	var ids []uint32
	for i, a := range t.stackPend {
		if q, ok := t.stackQty(g, a); !ok || q != t.pendQty {
			ch = append(ch, a)
			ids = append(ids, t.pendIDs[i])
		}
	}
	if len(ch) == 0 {
		return false, 0, errors.New("nothing changed, sell 1 of the item")
	}
	same := true
	for _, id := range ids {
		if id != ids[0] {
			same = false
		}
	}
	if same {
		t.stackSel, t.stackAlso, t.pendID = ch[0], ch[1:], ids[0]
		t.stackPend = nil
		return true, 1, nil
	}
	t.stackPend, t.pendIDs = ch, ids
	return false, len(ch), nil
}

// Found reports whether the stack has been identified.
func (t *Trainer) Found() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stackSel != 0
}

// BuyBackStep runs after the user bought the item back. When the stack is already identified it
// finds it again by id; otherwise it rebuilds the candidate list for the next sell round.
func (t *Trainer) BuyBackStep() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	g := t.game()
	if g == nil {
		return errors.New("start again")
	}
	if t.stackSel == 0 {
		keep := map[uint32]bool{}
		for _, id := range t.pendIDs {
			keep[id] = true
		}
		all, err := t.findStacks(g, uint32(t.pendQty), 0)
		if err != nil {
			return err
		}
		t.stackPend, t.pendIDs = nil, nil
		for _, a := range all {
			if id := t.stackID(g, a); keep[id] {
				t.stackPend = append(t.stackPend, a)
				t.pendIDs = append(t.pendIDs, id)
			}
		}
		if len(t.stackPend) == 0 {
			return errors.New("item not found, buy that 1 back first")
		}
		return nil
	}
	if q, ok := t.stackQty(g, t.stackSel); ok && q == t.pendQty && t.stackID(g, t.stackSel) == t.pendID {
		return nil
	}
	addrs, err := t.findStacks(g, uint32(t.pendQty), t.pendID)
	if err != nil {
		return err
	}
	if len(addrs) == 0 {
		return errors.New("item not found, buy that 1 back first")
	}
	// extra matches are stale copies of the same stack; AddStack writes all of them
	t.stackSel, t.stackAlso = addrs[0], addrs[1:]
	return nil
}

// AddStack adds add to the selected stack (add <= 0 doubles it). Returns the new amount.
func (t *Trainer) AddStack(add int64) (int64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	g := t.game()
	if g == nil || t.stackSel == 0 {
		return 0, errors.New("no item selected")
	}
	b, err := g.read(t.stackSel+0x14, 4)
	if err != nil || len(b) < 4 {
		return 0, errors.New("item is gone, start again")
	}
	cur := int64(binary.LittleEndian.Uint32(b))
	if add <= 0 {
		add = cur
	}
	nq := cur + add
	if nq > int32Max {
		nq = int32Max
	}
	var w [4]byte
	binary.LittleEndian.PutUint32(w[:], uint32(nq))
	if err := g.write(t.stackSel+0x14, w[:]); err != nil {
		return 0, err
	}
	for _, a := range t.stackAlso {
		_ = g.write(a+0x14, w[:])
	}
	t.Logf("stack %#x: %d -> %d", t.stackSel, cur, nq)
	return nq, nil
}

func (t *Trainer) StackReset() {
	t.mu.Lock()
	t.stackPend, t.stackSel, t.stackAlso = nil, 0, nil
	t.mu.Unlock()
}

// StackInfo returns id and amount of the selected stack.
func (t *Trainer) StackInfo() (id uint32, qty int64, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	g := t.game()
	if g == nil || t.stackSel == 0 {
		return 0, 0, errors.New("no item selected")
	}
	q, ok := t.stackQty(g, t.stackSel)
	if !ok {
		return 0, 0, errors.New("item is gone, start again")
	}
	return t.stackID(g, t.stackSel), q, nil
}

// ReplaceStack turns the selected stack into item id with amount qty.
func (t *Trainer) ReplaceStack(id uint32, qty int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	g := t.game()
	if g == nil || t.stackSel == 0 {
		return errors.New("no item selected")
	}
	if qty < 1 || qty > int32Max {
		return errors.New("bad amount")
	}
	var w [8]byte
	binary.LittleEndian.PutUint32(w[:], id)
	binary.LittleEndian.PutUint32(w[4:], uint32(qty))
	for _, a := range append([]uint64{t.stackSel}, t.stackAlso...) {
		if err := g.write(a+0x10, w[:]); err != nil {
			return err
		}
	}
	t.Logf("stack %#x replaced by id %d x%d", t.stackSel, id, qty)
	return nil
}
