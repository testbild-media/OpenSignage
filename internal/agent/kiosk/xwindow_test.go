package kiosk

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
)

type fakeWin struct {
	x, y, w, h int16
	mapState   byte // 2 = viewable
	override   bool
	gone       bool // requests on it fail with BadWindow
}

// fakeX answers the few requests fitWindows sends and records ConfigureWindow calls.
func fakeX(t *testing.T, c net.Conn, root uint32, wins map[uint32]*fakeWin, order []uint32, configured map[uint32][]uint32) {
	t.Helper()
	le := binary.LittleEndian
	defer c.Close()
	setup := make([]byte, 12)
	if _, err := io.ReadFull(c, setup); err != nil || setup[0] != 'l' || le.Uint16(setup[2:]) != 11 {
		t.Errorf("bad setup %v %v", setup, err)
		return
	}
	// Body: 32 fixed bytes, vendor "Test" (4), one format (8), one screen (40).
	body := make([]byte, 32+4+8+40)
	le.PutUint16(body[16:], 4)
	body[20], body[21] = 1, 1
	copy(body[32:], "Test")
	le.PutUint32(body[44:], root)
	head := []byte{1, 0, 11, 0, 0, 0, 0, 0}
	le.PutUint16(head[6:], uint16(len(body)/4))
	c.Write(append(head, body...))
	seq := uint16(0)
	for {
		h := make([]byte, 4)
		if _, err := io.ReadFull(c, h); err != nil {
			return
		}
		seq++
		rest := make([]byte, 4*int(le.Uint16(h[2:]))-4)
		if _, err := io.ReadFull(c, rest); err != nil {
			return
		}
		reply := make([]byte, 32)
		reply[0] = 1
		le.PutUint16(reply[2:], seq)
		var win uint32
		if len(rest) >= 4 {
			win = le.Uint32(rest)
		}
		fw := wins[win]
		if fw != nil && fw.gone {
			e := make([]byte, 32)
			e[1] = 3 // BadWindow
			c.Write(e)
			continue
		}
		switch h[0] {
		case 15: // QueryTree
			le.PutUint32(reply[4:], uint32(len(order)))
			le.PutUint16(reply[16:], uint16(len(order)))
			for _, id := range order {
				reply = le.AppendUint32(reply, id)
			}
		case 3: // GetWindowAttributes
			le.PutUint32(reply[4:], 3)
			reply = append(reply, make([]byte, 12)...)
			reply[26] = fw.mapState
			if fw.override {
				reply[27] = 1
			}
		case 14: // GetGeometry
			le.PutUint16(reply[12:], uint16(fw.x))
			le.PutUint16(reply[14:], uint16(fw.y))
			le.PutUint16(reply[16:], uint16(fw.w))
			le.PutUint16(reply[18:], uint16(fw.h))
		case 12: // ConfigureWindow: no reply
			if le.Uint16(h[2:]) != 7 || le.Uint16(rest[4:]) != 0x000f {
				t.Errorf("bad ConfigureWindow %v %v", h, rest)
			}
			configured[win] = []uint32{le.Uint32(rest[8:]), le.Uint32(rest[12:]), le.Uint32(rest[16:]), le.Uint32(rest[20:])}
			continue
		case 43: // GetInputFocus
		default:
			t.Errorf("unexpected request %d", h[0])
			return
		}
		c.Write(reply)
	}
}

func TestFitWindowsResizesTheOnePixelSmallerWindow(t *testing.T) {
	wins := map[uint32]*fakeWin{
		0x200002: {0, 0, 1919, 1079, 2, false, false}, // Chromium's window → fixed
		0x600001: {10, 10, 10, 10, 2, false, false},   // Chromium's helper window
		0x200100: {0, 0, 1919, 1079, 0, false, false}, // not mapped
		0x200200: {0, 0, 1919, 1079, 2, true, false},  // override-redirect (popup)
		0x200300: {0, 0, 1280, 720, 2, false, false},  // far smaller: not ours to touch
		0x200400: {0, 0, 1919, 1079, 2, false, true},  // went away meanwhile
	}
	order := []uint32{0x600001, 0x200100, 0x200200, 0x200300, 0x200400, 0x200002}
	configured := map[uint32][]uint32{}
	a, b := net.Pipe()
	done := make(chan struct{})
	go func() { fakeX(t, b, 0x1d5, wins, order, configured); close(done) }()
	res, err := fitWindows(a, 1920, 1080)
	a.Close()
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if res.Fixed != 1 || res.Exact != 0 {
		t.Fatalf("result %+v", res)
	}
	if got := configured[0x200002]; len(got) != 4 || got[0] != 0 || got[1] != 0 || got[2] != 1920 || got[3] != 1080 {
		t.Fatalf("configured %v", configured)
	}
	if len(configured) != 1 {
		t.Fatalf("other windows touched: %v", configured)
	}
}

func TestFitWindowsLeavesAnExactWindowAlone(t *testing.T) {
	wins := map[uint32]*fakeWin{0x200002: {0, 0, 3840, 2160, 2, false, false}}
	configured := map[uint32][]uint32{}
	a, b := net.Pipe()
	done := make(chan struct{})
	go func() { fakeX(t, b, 0x1d5, wins, []uint32{0x200002}, configured); close(done) }()
	res, err := fitWindows(a, 3840, 2160)
	a.Close()
	<-done
	if err != nil || res.Fixed != 0 || res.Exact != 1 || len(configured) != 0 {
		t.Fatalf("res %+v err %v configured %v", res, err, configured)
	}
}

func TestFitWindowsRejectsBadSizes(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if _, err := fitWindows(a, 0, 1080); err == nil {
		t.Fatal("want error for an unknown screen size")
	}
}
