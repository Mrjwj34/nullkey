//go:build darwin && cgo

package native

/*
#cgo CFLAGS: -Wno-deprecated-declarations
#cgo LDFLAGS: -lsandbox
#include <sandbox.h>
#include <stdlib.h>

static int nk_sandbox_init(const char *profile, char **errbuf) {
	return sandbox_init(profile, 0, errbuf);
}
static void nk_sandbox_free(char *errbuf) {
	if (errbuf != NULL) {
		sandbox_free_error(errbuf);
	}
}
*/
import "C"

import (
	"errors"
	"unsafe"
)

func sandboxAvailable() bool { return true }

func applySandbox(profile string) error {
	cs := C.CString(profile)
	defer C.free(unsafe.Pointer(cs))
	var errbuf *C.char
	if rc := C.nk_sandbox_init(cs, &errbuf); rc != 0 {
		msg := "sandbox_init failed"
		if errbuf != nil {
			msg = C.GoString(errbuf)
			C.nk_sandbox_free(errbuf)
		}
		return errors.New(msg)
	}
	return nil
}
