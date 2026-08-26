// detach.go severs a rod Browser's CDP WebSocket without sending the
// Browser.close command. rod's Browser.Close always issues Browser.close,
// which shuts the whole browser process down — correct for a Chrome whip
// launched, wrong for one we only attached to (live mode, or a reattached
// dedicated Chrome another backend still owns). Detaching lets such
// backends drop their connection while leaving the process alive.
package browser

import (
	"reflect"
	"unsafe"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
)

// detach closes rb's underlying CDP WebSocket. The browser process is NOT
// told to shut down; our connection simply goes away. Returns false when the
// socket could not be reached (rod changed shape) — callers should treat
// that as a visible anomaly, since the connection then leaks.
func detach(rb *rod.Browser) bool {
	if rb == nil {
		return true
	}
	// rod.Browser.client is unexported; reach it by reflection. Kind-guarded
	// (IsNil panics on non-nilable kinds) so a rod upgrade that renames or
	// retypes the field degrades to a reported false rather than a panic.
	f := reflect.ValueOf(rb).Elem().FieldByName("client")
	if !nilable(f) || f.IsNil() {
		return false
	}
	client, ok := reflect.TypeAssert[*cdp.Client](reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()) //nolint:gosec // G103: deliberate reflect-into-unexported-field to reach rod's socket
	if !ok || client == nil {
		return false
	}
	// cdp.Client.ws is likewise unexported; its Close severs the socket.
	wf := reflect.ValueOf(client).Elem().FieldByName("ws")
	if !nilable(wf) || wf.IsNil() {
		return false
	}
	ws, ok := reflect.TypeAssert[*cdp.WebSocket](reflect.NewAt(wf.Type(), unsafe.Pointer(wf.UnsafeAddr())).Elem()) //nolint:gosec // G103: same deliberate reflect-into-unexported-field as above
	if !ok || ws == nil {
		return false
	}
	_ = ws.Close()
	return true
}

// nilable reports whether v is valid and of a kind IsNil accepts
// (chan/func/interface/map/ptr/slice) — IsNil panics otherwise.
func nilable(v reflect.Value) bool {
	if !v.IsValid() {
		return false
	}
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return true
	}
	return false
}
