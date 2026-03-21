package main

// Minimal zwlr_layer_shell_v1 and zwlr_layer_surface_v1 bindings.
// The go-wayland library does not include wlr-protocols, so we implement
// just what we need using the same raw WriteMsg pattern as registryBind.

import "github.com/rajveermalviya/go-wayland/wayland/client"

const (
	layerBackground           uint32 = 0
	keyboardInteractivityNone uint32 = 0
)

// LayerShell wraps zwlr_layer_shell_v1.
type LayerShell struct {
	client.BaseProxy
}

func NewLayerShell(ctx *client.Context) *LayerShell {
	s := &LayerShell{}
	ctx.Register(s)
	return s
}

// Dispatch handles compositor→client events (none for this manager).
func (s *LayerShell) Dispatch(_ uint32, _ int, _ []byte) {}

// GetLayerSurface sends get_layer_surface (opcode 0).
// output=null means the compositor chooses the output.
func (s *LayerShell) GetLayerSurface(surface *client.Surface, layer uint32, namespace string) (*LayerSurface, error) {
	id := NewLayerSurface(s.Context())

	// Wayland string: length field = len(s)+1 (actual, includes null), data padded to 4 bytes.
	nsActual := len(namespace) + 1
	nsPadded := (nsActual + 3) &^ 3

	msgLen := 8 + 4 + 4 + 4 + 4 + 4 + nsPadded
	buf := make([]byte, msgLen)
	off := 0
	client.PutUint32(buf[off:], s.ID())
	off += 4
	client.PutUint32(buf[off:], uint32(0|(msgLen<<16))) // opcode 0
	off += 4
	client.PutUint32(buf[off:], id.ID())
	off += 4
	client.PutUint32(buf[off:], surface.ID())
	off += 4
	client.PutUint32(buf[off:], 0) // output: null
	off += 4
	client.PutUint32(buf[off:], layer)
	off += 4
	client.PutUint32(buf[off:], uint32(nsActual)) // length including null
	off += 4
	copy(buf[off:], namespace) // null byte + padding already zero

	return id, s.Context().WriteMsg(buf, nil)
}

// LayerSurface wraps zwlr_layer_surface_v1.
type LayerSurface struct {
	client.BaseProxy
	onConfigure func(serial, width, height uint32)
}

func NewLayerSurface(ctx *client.Context) *LayerSurface {
	s := &LayerSurface{}
	ctx.Register(s)
	return s
}

func (s *LayerSurface) SetConfigureHandler(f func(serial, width, height uint32)) {
	s.onConfigure = f
}

// Dispatch handles layer surface events.
func (s *LayerSurface) Dispatch(opcode uint32, _ int, data []byte) {
	switch opcode {
	case 0: // configure(serial, width, height)
		if len(data) >= 12 && s.onConfigure != nil {
			s.onConfigure(
				client.Uint32(data[0:4]),
				client.Uint32(data[4:8]),
				client.Uint32(data[8:12]),
			)
		}
	// case 1: closed — compositor closed the surface; we'll exit via dispatchErr
	}
}

// SetSize sends set_size (opcode 0).
func (s *LayerSurface) SetSize(width, height uint32) error {
	const msgLen = 8 + 4 + 4
	var buf [msgLen]byte
	client.PutUint32(buf[0:], s.ID())
	client.PutUint32(buf[4:], uint32(0|(msgLen<<16)))
	client.PutUint32(buf[8:], width)
	client.PutUint32(buf[12:], height)
	return s.Context().WriteMsg(buf[:], nil)
}

// SetKeyboardInteractivity sends set_keyboard_interactivity (opcode 4).
func (s *LayerSurface) SetKeyboardInteractivity(v uint32) error {
	const msgLen = 8 + 4
	var buf [msgLen]byte
	client.PutUint32(buf[0:], s.ID())
	client.PutUint32(buf[4:], uint32(4|(msgLen<<16)))
	client.PutUint32(buf[8:], v)
	return s.Context().WriteMsg(buf[:], nil)
}

// AckConfigure sends ack_configure (opcode 6).
func (s *LayerSurface) AckConfigure(serial uint32) error {
	const msgLen = 8 + 4
	var buf [msgLen]byte
	client.PutUint32(buf[0:], s.ID())
	client.PutUint32(buf[4:], uint32(6|(msgLen<<16)))
	client.PutUint32(buf[8:], serial)
	return s.Context().WriteMsg(buf[:], nil)
}
