//go:build darwin && cgo

package hotkey

/*
#include <stdlib.h>
*/
import "C"

//export frpRunStatusInput
func frpRunStatusInput(id C.int, text *C.char) {
	sendRunStatusInput(int(id), C.GoString(text))
}

//export frpRunStatusUserClosed
func frpRunStatusUserClosed(id C.int) {
	markRunStatusUserClosed(int(id))
}
