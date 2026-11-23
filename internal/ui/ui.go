package ui

import (
	"fmt"
	"strconv"
	"sync"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"

	"gof2remake-trainer/internal/trainer"
)

const (
	ksF10       = 0xffc7
	ksEscape    = 0xff1b
	ksBackSpace = 0xff08
	ksReturn    = 0xff0d
	ksTab       = 0xff09
	ksZero      = 0x30
	ksOne       = 0x31
	ksTwo       = 0x32
	ksThree     = 0x33
	ksFour      = 0x34
	ksFive      = 0x35
	ksUp        = 0xff52
	ksDown      = 0xff54
	ksPgUp      = 0xff55
	ksPgDn      = 0xff56
	winW        = 520
	winH        = 300
	listRows    = 9
)

type UI struct {
	t       *trainer.Trainer
	conn    *xgb.Conn
	root    xproto.Window
	win     xproto.Window
	gc      xproto.Gcontext
	keys    map[uint32]xproto.Keycode
	mu      sync.Mutex
	open    bool
	page    int // 0 main, 1 money, 2 cargo, 3 spawn
	buf     string
	stage   int // cargo wizard: 0 intro, 1 amount, 2 sell, 3 buy back, 4 add
	msg     string
	grabbed []uint32
	dataDir string
	cat     []trainer.CatItem
	catErr  string
	catSel  int
	items   []trainer.CatItem
	sel     int
	pick    trainer.CatItem
	spQty   int64
}

var lockMods = []uint16{0, xproto.ModMaskLock, xproto.ModMask2, xproto.ModMaskLock | xproto.ModMask2}

func Run(t *trainer.Trainer, dataDir string) {
	u := &UI{t: t, dataDir: dataDir}
	var err error
	if u.conn, err = xgb.NewConn(); err != nil {
		t.Logf("ui: x11 connect: %v", err)
		return
	}
	defer u.conn.Close()
	scr := xproto.Setup(u.conn).DefaultScreen(u.conn)
	u.root = scr.Root
	if err := u.loadKeys(); err != nil {
		t.Logf("ui: keymap: %v", err)
		return
	}
	u.win, _ = xproto.NewWindowId(u.conn)
	xproto.CreateWindow(u.conn, scr.RootDepth, u.win, u.root, 40, 40, winW, winH, 2,
		xproto.WindowClassInputOutput, scr.RootVisual,
		xproto.CwBackPixel|xproto.CwBorderPixel|xproto.CwOverrideRedirect|xproto.CwEventMask,
		[]uint32{0x101010, 0x2060c0, 1, xproto.EventMaskExposure})
	font, _ := xproto.NewFontId(u.conn)
	opened := false
	for _, n := range []string{"9x15bold", "9x15", "10x20", "fixed"} {
		if xproto.OpenFontChecked(u.conn, font, uint16(len(n)), n).Check() == nil {
			opened = true
			break
		}
	}
	if !opened {
		t.Logf("ui: no usable core font")
		return
	}
	u.gc, _ = xproto.NewGcontextId(u.conn)
	xproto.CreateGC(u.conn, u.gc, xproto.Drawable(u.win),
		xproto.GcForeground|xproto.GcBackground|xproto.GcFont,
		[]uint32{0xf0f0f0, 0x101010, uint32(font)})
	if err := u.grab(ksF10); err != nil {
		t.Logf("ui: cannot grab F10: %v", err)
		return
	}
	t.Logf("ui: ready, F10 grabbed")
	for {
		ev, err := u.conn.WaitForEvent()
		if err != nil {
			t.Logf("ui: x error: %v", err)
			continue
		}
		if ev == nil {
			return
		}
		switch e := ev.(type) {
		case xproto.ExposeEvent:
			u.mu.Lock()
			u.drawLocked()
			u.mu.Unlock()
		case xproto.KeyPressEvent:
			u.onKey(e.Detail)
		}
	}
}

func (u *UI) loadKeys() error {
	setup := xproto.Setup(u.conn)
	n := byte(setup.MaxKeycode-setup.MinKeycode) + 1
	m, err := xproto.GetKeyboardMapping(u.conn, setup.MinKeycode, n).Reply()
	if err != nil {
		return err
	}
	u.keys = map[uint32]xproto.Keycode{}
	per := int(m.KeysymsPerKeycode)
	for i := 0; i < int(n); i++ {
		ks := uint32(m.Keysyms[i*per])
		if _, ok := u.keys[ks]; !ok {
			u.keys[ks] = setup.MinKeycode + xproto.Keycode(i)
		}
	}
	return nil
}

func (u *UI) grab(ks uint32) error {
	kc, ok := u.keys[ks]
	if !ok {
		return fmt.Errorf("keysym %#x not on keyboard", ks)
	}
	for _, m := range lockMods {
		if err := xproto.GrabKeyChecked(u.conn, true, u.root, m, kc, xproto.GrabModeAsync, xproto.GrabModeAsync).Check(); err != nil {
			return err
		}
	}
	return nil
}

func (u *UI) ungrab(ks uint32) {
	if kc, ok := u.keys[ks]; ok {
		for _, m := range lockMods {
			xproto.UngrabKey(u.conn, kc, u.root, m)
		}
	}
}

// spawn wizard stages (page 3)
const (
	spCat = iota
	spList
	spAmt
	spInfo
	spFind
	spSell
	spBuy
	spConfirm
	spDone
)

func (u *UI) wanted() []uint32 {
	ks := []uint32{ksEscape}
	digits := func() {
		ks = append(ks, ksBackSpace, ksReturn, ksTab)
		for d := uint32(0); d < 10; d++ {
			ks = append(ks, ksZero+d)
		}
	}
	switch u.page {
	case 0:
		ks = append(ks, ksOne, ksTwo, ksThree)
	case 3:
		switch u.stage {
		case spCat:
			ks = append(ks, ksOne, ksTwo, ksThree, ksFour, ksFive)
		case spList:
			ks = append(ks, ksReturn, ksUp, ksDown, ksPgUp, ksPgDn)
		case spAmt, spFind:
			digits()
		default:
			ks = append(ks, ksReturn)
		}
	default:
		digits()
	}
	return ks
}

func (u *UI) regrab() {
	for _, k := range u.grabbed {
		u.ungrab(k)
	}
	u.grabbed = nil
	if !u.open {
		return
	}
	for _, k := range u.wanted() {
		if u.grab(k) == nil {
			u.grabbed = append(u.grabbed, k)
		}
	}
}

func (u *UI) goPage(p int) {
	u.page, u.buf, u.msg, u.stage = p, "", "", 0
	u.t.StackReset()
	if p == 3 && u.cat == nil && u.catErr == "" {
		c, err := trainer.LoadCatalog(u.dataDir)
		if err != nil {
			u.catErr = err.Error()
		}
		u.cat = c
	}
	u.regrab()
}

func (u *UI) setStage(st int) {
	u.stage, u.buf = st, ""
	u.regrab()
}

func (u *UI) onKey(code xproto.Keycode) {
	u.mu.Lock()
	defer u.mu.Unlock()
	is := func(ks uint32) bool { return code == u.keys[ks] }
	switch {
	case is(ksF10):
		if u.open {
			u.hide()
		} else {
			u.show()
		}
		return
	case !u.open:
		return
	case u.page == 0:
		switch {
		case is(ksEscape):
			u.hide()
			return
		case is(ksOne):
			u.goPage(1)
		case is(ksTwo):
			u.goPage(2)
		case is(ksThree):
			u.goPage(3)
		}
	case u.page == 3:
		u.spawnKey(is)
	default:
		switch {
		case is(ksEscape):
			u.goPage(0)
		case is(ksBackSpace):
			if len(u.buf) > 0 {
				u.buf = u.buf[:len(u.buf)-1]
			}
		case is(ksTab):
			if u.page == 1 {
				u.t.Reset()
				u.msg = "money OFF"
			} else {
				u.t.StackReset()
				u.msg = "reset"
			}
		case is(ksReturn):
			if u.page == 1 {
				u.money()
			} else {
				u.cargoStep()
			}
		default:
			u.typeDigit(is)
		}
	}
	u.drawLocked()
}

func (u *UI) typeDigit(is func(uint32) bool) {
	for d := uint32(0); d < 10; d++ {
		if is(ksZero+d) && len(u.buf) < 12 {
			u.buf += string(rune('0' + d))
		}
	}
}

func (u *UI) spawnKey(is func(uint32) bool) {
	if is(ksEscape) {
		switch {
		case u.stage == spCat:
			u.goPage(0)
		case u.stage == spAmt:
			u.setStage(spList)
		default:
			u.t.StackReset()
			u.msg = ""
			u.setStage(spCat)
		}
		return
	}
	switch u.stage {
	case spCat:
		for i := range trainer.CatNames {
			if is(ksOne + uint32(i)) {
				if u.cat == nil {
					return
				}
				u.items = u.items[:0]
				for _, it := range u.cat {
					if it.Cat == i {
						u.items = append(u.items, it)
					}
				}
				u.sel = 0
				u.setStage(spList)
			}
		}
	case spList:
		switch {
		case is(ksUp):
			if u.sel > 0 {
				u.sel--
			}
		case is(ksDown):
			if u.sel < len(u.items)-1 {
				u.sel++
			}
		case is(ksPgUp):
			u.sel = max(0, u.sel-listRows)
		case is(ksPgDn):
			u.sel = min(len(u.items)-1, u.sel+listRows)
		case is(ksReturn):
			u.pick = u.items[u.sel]
			u.setStage(spAmt)
		}
	case spAmt:
		switch {
		case is(ksBackSpace):
			if len(u.buf) > 0 {
				u.buf = u.buf[:len(u.buf)-1]
			}
		case is(ksReturn):
			if v, ok := u.number(); ok && v >= 1 && v <= 2147483647 {
				u.spQty = v
				u.msg = ""
				u.setStage(spInfo)
			} else {
				u.msg = "type an amount from 1"
			}
		default:
			u.typeDigit(is)
		}
	case spInfo:
		if is(ksReturn) {
			u.msg = ""
			u.setStage(spFind)
		}
	case spFind:
		switch {
		case is(ksBackSpace):
			if len(u.buf) > 0 {
				u.buf = u.buf[:len(u.buf)-1]
			}
		case is(ksReturn):
			v, ok := u.number()
			if !ok {
				return
			}
			u.msg = "scanning..."
			u.drawLocked()
			pend, err := u.t.FindStack(v)
			switch {
			case err != nil:
				u.msg = err.Error()
			case pend > 0:
				u.msg = ""
				u.setStage(spSell)
			default:
				u.msg = ""
				u.setStage(spConfirm)
			}
		default:
			u.typeDigit(is)
		}
	case spSell:
		if is(ksReturn) {
			_, left, err := u.t.SellStep()
			if err != nil {
				u.msg = err.Error()
				return
			}
			u.msg = ""
			if left > 1 {
				u.msg = fmt.Sprintf("%d candidates left", left)
			}
			u.setStage(spBuy)
		}
	case spBuy:
		if is(ksReturn) {
			if err := u.t.BuyBackStep(); err != nil {
				u.msg = err.Error()
				return
			}
			u.msg = ""
			if u.t.Found() {
				u.setStage(spConfirm)
			} else {
				u.setStage(spSell)
			}
		}
	case spConfirm:
		if is(ksReturn) {
			if err := u.t.ReplaceStack(uint32(u.pick.ID), u.spQty); err != nil {
				u.msg = err.Error()
				return
			}
			u.msg = ""
			u.setStage(spDone)
		}
	case spDone:
		if is(ksReturn) {
			u.setStage(spCat)
		}
	}
}

func (u *UI) name(id uint32) string {
	for _, it := range u.cat {
		if uint32(it.ID) == id {
			return it.Name
		}
	}
	return fmt.Sprintf("item #%d", id)
}

func (u *UI) number() (int64, bool) {
	v, err := strconv.ParseInt(u.buf, 10, 64)
	if err != nil {
		u.msg = "type a number first"
		return 0, false
	}
	return v, true
}

func (u *UI) money() {
	v, ok := u.number()
	if !ok {
		return
	}
	u.msg = "scanning..."
	u.drawLocked()
	u.t.Reset()
	n, err := u.t.Search(v)
	u.buf = ""
	if err != nil {
		u.msg = err.Error()
		return
	}
	if n == 0 {
		u.msg = "not found, check the number"
		return
	}
	if err := u.t.SetMoney(true); err != nil {
		u.msg = err.Error()
		return
	}
	u.msg = fmt.Sprintf("money ON (%d places)", n)
}

func (u *UI) cargoStep() {
	switch u.stage {
	case 0:
		u.stage, u.buf = 1, ""
		return
	case 2:
		_, left, err := u.t.SellStep()
		if err != nil {
			u.msg = err.Error()
			return
		}
		u.stage, u.msg = 3, ""
		if left > 1 {
			u.msg = fmt.Sprintf("%d candidates left", left)
		}
		return
	case 3:
		if err := u.t.BuyBackStep(); err != nil {
			u.msg = err.Error()
			return
		}
		u.msg = ""
		if u.t.Found() {
			u.stage = 4
		} else {
			u.stage = 2
		}
		return
	case 4:
		var add int64
		if u.buf != "" {
			add, _ = strconv.ParseInt(u.buf, 10, 64)
		}
		u.buf = ""
		n, err := u.t.AddStack(add)
		if err != nil {
			u.msg = err.Error()
			return
		}
		u.msg = fmt.Sprintf("now %d. Reopen the screen", n)
		return
	}
	v, ok := u.number()
	if !ok {
		return
	}
	u.msg = "scanning..."
	u.drawLocked()
	pend, err := u.t.FindStack(v)
	u.buf = ""
	switch {
	case err != nil:
		u.msg = err.Error()
	case pend > 0:
		u.stage, u.msg = 2, ""
	default:
		u.stage, u.msg = 4, ""
	}
}

func (u *UI) show() {
	u.open, u.page, u.msg, u.buf, u.stage = true, 0, "", "", 0
	xproto.MapWindow(u.conn, u.win)
	xproto.ConfigureWindow(u.conn, u.win, xproto.ConfigWindowStackMode, []uint32{xproto.StackModeAbove})
	u.regrab()
	u.drawLocked()
}

func (u *UI) hide() {
	u.open = false
	xproto.UnmapWindow(u.conn, u.win)
	u.regrab()
}

func (u *UI) text(y int16, s string) {
	if len(s) > 255 {
		s = s[:255]
	}
	xproto.ImageText8(u.conn, byte(len(s)), xproto.Drawable(u.win), u.gc, 12, y, s)
}

func (u *UI) drawLocked() {
	if !u.open {
		return
	}
	xproto.ClearArea(u.conn, false, u.win, 0, 0, 0, 0)
	switch u.page {
	case 1:
		state := "OFF"
		if u.t.Locked() {
			state = "ON"
		}
		u.text(22, "MONEY 9,999,999,999: "+state+"   [Esc] back")
		u.text(48, "your credits now: "+u.buf+"_")
		u.text(70, "Enter = ON, Tab = OFF")
	case 2:
		u.text(22, "CARGO DUPLICATE   [Esc] back")
		switch u.stage {
		case 0:
			u.text(48, "Step 1: open the trade screen")
			u.text(70, "Step 2: click the item to double")
			u.text(92, "[Enter] next")
			return
		case 1:
			u.text(48, "Step 3: type SHIP amount of that item")
			u.text(70, "amount: "+u.buf+"_   [Enter] next")
		case 2:
			u.text(48, "Other items share it. Step 4:")
			u.text(70, "SELL 1 of that item, then [Enter]")
		case 3:
			u.text(48, "Step 5: BUY that 1 back")
			u.text(70, "then [Enter]")
		case 4:
			u.text(48, "Item found")
			u.text(70, "add how many: "+u.buf+"_  (empty = double)")
			u.text(114, "[Enter] apply, again to add more")
		}
	case 3:
		u.drawSpawn()
		return
	default:
		u.text(22, "GoF2Remake Trainer   [Esc] close")
		u.text(48, "[1] Money")
		u.text(70, "[2] Cargo duplicate")
		u.text(92, "[3] Item spawner")
		return
	}
	u.text(92, u.msg)
}

func (u *UI) drawSpawn() {
	if u.catErr != "" || u.cat == nil {
		u.text(22, "ITEM SPAWNER   [Esc] back")
		u.text(48, "catalog error: "+u.catErr)
		return
	}
	switch u.stage {
	case spCat:
		u.text(22, "ITEM SPAWNER: type   [Esc] back")
		for i, n := range trainer.CatNames {
			u.text(int16(48+22*i), fmt.Sprintf("[%d] %s", i+1, n))
		}
	case spList:
		u.text(22, fmt.Sprintf("%s (%d)   [Esc] back", trainer.CatNames[u.items[0].Cat], len(u.items)))
		first := 0
		if u.sel >= listRows {
			first = u.sel - listRows + 1
		}
		for r := 0; r < listRows && first+r < len(u.items); r++ {
			mark := "  "
			if first+r == u.sel {
				mark = "> "
			}
			u.text(int16(48+22*r), mark+u.items[first+r].Name)
		}
		u.text(48+22*listRows, "Up/Down, PgUp/PgDn, [Enter] select")
	case spAmt:
		u.text(22, "ITEM SPAWNER: amount   [Esc] back")
		u.text(48, u.pick.Name)
		u.text(70, "amount: "+u.buf+"_   [Enter] next")
	case spInfo:
		u.text(22, "SPAWN "+u.pick.Name+" x"+strconv.FormatInt(u.spQty, 10))
		u.text(48, "WARNING: one of your cargo items will be")
		u.text(70, "REPLACED by this item (it is lost).")
		u.text(92, "Open the trade screen, scroll to the "+trainer.CatNames[u.pick.Cat])
		u.text(114, "group, click a cheap item of that same")
		u.text(136, "type you can lose, then [Enter]")
	case spFind:
		u.text(22, "Which item is replaced?   [Esc] cancel")
		u.text(48, "type its SHIP amount (trade screen)")
		u.text(70, "amount: "+u.buf+"_   [Enter] next")
	case spSell:
		u.text(22, "Other items share it.   [Esc] cancel")
		u.text(48, "SELL 1 of the item to replace,")
		u.text(70, "then [Enter]")
	case spBuy:
		u.text(22, "Almost there.   [Esc] cancel")
		u.text(48, "BUY that 1 back, then [Enter]")
	case spConfirm:
		id, qty, err := u.t.StackInfo()
		u.text(22, "CONFIRM   [Esc] cancel")
		if err != nil {
			u.text(48, err.Error())
			break
		}
		u.text(48, "REPLACE: "+u.name(id)+" x"+strconv.FormatInt(qty, 10))
		u.text(70, "WITH:    "+u.pick.Name+" x"+strconv.FormatInt(u.spQty, 10))
		u.text(114, "[Enter] confirm   [Esc] cancel")
	case spDone:
		u.text(22, "DONE")
		u.text(48, u.pick.Name+" x"+strconv.FormatInt(u.spQty, 10)+" added")
		u.text(70, "Click another item and back, or reopen")
		u.text(92, "the screen. [Enter] spawn another")
	}
	u.text(int16(winH-20), u.msg)
}
