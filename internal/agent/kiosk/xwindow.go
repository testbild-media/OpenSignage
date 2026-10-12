package kiosk

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// Chromium on X11 never opens a window exactly as large as the screen: a requested size
// equal to the screen becomes one pixel narrower and lower (1919×1079 on a 1920×1080
// screen). Without a window manager nothing corrects that, so the stage is scaled by
// 0.9995 and a one-pixel stripe stays black at the right and bottom. A resize from
// outside is accepted, so the agent sets the window to the screen size itself – with
// a few X11 requests on the local socket (Xorg runs without TCP and without auth),
// no extra package needed on devices that only get in-app updates.

// xFitResult says what fitWindows found.
type xFitResult struct {
	Fixed int // windows resized to the screen
	Exact int // windows already exactly as large as the screen
}

// xFitWindows connects to the local X display and fits Chromium's window to w×h.
func xFitWindows(display string, w, h int) (xFitResult, error) {
	n := strings.TrimPrefix(display, ":")
	if i := strings.IndexByte(n, '.'); i >= 0 {
		n = n[:i]
	}
	var c net.Conn
	var err error
	for _, addr := range []string{"/tmp/.X11-unix/X" + n, "@/tmp/.X11-unix/X" + n} {
		if c, err = net.DialTimeout("unix", addr, 2*time.Second); err == nil {
			break
		}
	}
	if err != nil {
		return xFitResult{}, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	return fitWindows(c, w, h)
}

var le = binary.LittleEndian

// fitWindows speaks X11 on conn: viewable top-level windows at 0,0 that are at most two
// pixels smaller than w×h (and not equal) get exactly w×h.
func fitWindows(conn io.ReadWriter, w, h int) (xFitResult, error) {
	var res xFitResult
	if w <= 0 || h <= 0 || w > 0xffff || h > 0xffff {
		return res, fmt.Errorf("invalid screen size %dx%d", w, h)
	}
	x := &xconn{rw: conn}
	root, err := x.setup()
	if err != nil {
		return res, err
	}
	tree, err := x.request(15, root, nil) // QueryTree
	if err != nil {
		return res, err
	}
	if len(tree) < 32 {
		return res, errors.New("x11: short QueryTree reply")
	}
	count := int(le.Uint16(tree[16:]))
	if len(tree) < 32+4*count {
		return res, errors.New("x11: short QueryTree children")
	}
	for i := 0; i < count; i++ {
		win := le.Uint32(tree[32+4*i:])
		attr, err := x.request(3, win, nil) // GetWindowAttributes
		if err != nil {
			continue // the window went away in the meantime
		}
		if len(attr) < 28 || attr[26] != 2 || attr[27] != 0 { // viewable, not override-redirect
			continue
		}
		geo, err := x.request(14, win, nil) // GetGeometry
		if err != nil || len(geo) < 20 {
			continue
		}
		gx, gy := int16(le.Uint16(geo[12:])), int16(le.Uint16(geo[14:]))
		gw, gh := int(le.Uint16(geo[16:])), int(le.Uint16(geo[18:]))
		if gx != 0 || gy != 0 {
			continue
		}
		switch {
		case gw == w && gh == h:
			res.Exact++
		case gw >= w-2 && gw <= w && gh >= h-2 && gh <= h:
			// ConfigureWindow: value mask x|y|width|height, then x=0, y=0, w, h.
			vals := make([]byte, 20)
			le.PutUint16(vals[0:], 0x000f)
			le.PutUint32(vals[12:], uint32(w))
			le.PutUint32(vals[16:], uint32(h))
			if err := x.send(12, win, vals); err != nil {
				return res, err
			}
			// A round trip makes sure the resize was processed (an error would come first).
			if _, err := x.request(43, 0, nil); err != nil { // GetInputFocus
				return res, err
			}
			res.Fixed++
		}
	}
	return res, nil
}

type xconn struct {
	rw io.ReadWriter
}

// setup performs the connection handshake (no authorization) and returns the root
// window of the first screen.
func (x *xconn) setup() (uint32, error) {
	req := []byte{'l', 0, 11, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if _, err := x.rw.Write(req); err != nil {
		return 0, err
	}
	head := make([]byte, 8)
	if _, err := io.ReadFull(x.rw, head); err != nil {
		return 0, err
	}
	body := make([]byte, 4*int(le.Uint16(head[6:])))
	if _, err := io.ReadFull(x.rw, body); err != nil {
		return 0, err
	}
	if head[0] != 1 {
		reason := ""
		if head[0] == 0 && int(head[1]) <= len(body) {
			reason = string(body[:head[1]])
		}
		return 0, fmt.Errorf("x11: connection refused: %s", strings.TrimSpace(reason))
	}
	if len(body) < 32 {
		return 0, errors.New("x11: short setup reply")
	}
	vendor := int(le.Uint16(body[16:]))
	formats := int(body[21])
	off := 32 + (vendor+3)/4*4 + 8*formats
	if body[20] == 0 || len(body) < off+4 {
		return 0, errors.New("x11: no screen")
	}
	return le.Uint32(body[off:]), nil
}

// send writes one request: opcode, a window (or 0 for none) and extra data.
func (x *xconn) send(opcode byte, win uint32, extra []byte) error {
	words := 1
	if win != 0 {
		words++
	}
	words += len(extra) / 4
	b := make([]byte, 4, 4*words)
	b[0] = opcode
	le.PutUint16(b[2:], uint16(words))
	if win != 0 {
		b = le.AppendUint32(b, win)
	}
	b = append(b, extra...)
	_, err := x.rw.Write(b)
	return err
}

// request sends a request with a reply and returns the whole reply. Events are skipped
// (none are selected); an X error is returned as error.
func (x *xconn) request(opcode byte, win uint32, extra []byte) ([]byte, error) {
	if err := x.send(opcode, win, extra); err != nil {
		return nil, err
	}
	for {
		b := make([]byte, 32)
		if _, err := io.ReadFull(x.rw, b); err != nil {
			return nil, err
		}
		switch b[0] {
		case 0:
			return nil, fmt.Errorf("x11: error %d on request %d", b[1], opcode)
		case 1:
			if more := 4 * int(le.Uint32(b[4:])); more > 0 {
				if more > 1<<20 {
					return nil, errors.New("x11: reply too large")
				}
				rest := make([]byte, more)
				if _, err := io.ReadFull(x.rw, rest); err != nil {
					return nil, err
				}
				b = append(b, rest...)
			}
			return b, nil
		}
	}
}
