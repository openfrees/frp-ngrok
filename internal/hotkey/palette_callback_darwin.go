//go:build darwin && cgo

package hotkey

/*
#include <stdlib.h>
*/
import "C"

//export frpPaletteCommandSelected
func frpPaletteCommandSelected(cID *C.char) {
	id := C.GoString(cID)
	paletteState.Lock()
	item, ok := paletteState.items[id]
	dispatch := paletteState.dispatch
	paletteState.Unlock()
	if !ok || dispatch == nil {
		return
	}
	go dispatch(item)
}
