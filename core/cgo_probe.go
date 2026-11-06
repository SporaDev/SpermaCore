//go:build cgo

package main

// test probe helpers invoking cgo callbacks within active scope

/*
#include <stdlib.h>
#include <string.h>

extern char* goConfigGetString(char* key, char* defaultValue);
extern int   goConfigGetInt(char* key, int defaultValue);
extern char* goDataDir(void);

static void sc_probe_config_string(const char* key, char* out, size_t n) {
    char* v = goConfigGetString((char*)key, (char*)"");
    if (!v) { out[0] = 0; return; }
    strncpy(out, v, n - 1);
    out[n - 1] = 0;
}
static int sc_probe_config_int(const char* key, int def) {
    return goConfigGetInt((char*)key, def);
}
static void sc_probe_data_dir(char* out, size_t n) {
    char* v = goDataDir();
    if (!v) { out[0] = 0; return; }
    strncpy(out, v, n - 1);
    out[n - 1] = 0;
    free(v);
}
*/
import "C"

import "unsafe"

func cStringProbe(key string) string {
	ck := C.CString(key)
	defer C.free(unsafe.Pointer(ck))
	buf := make([]byte, 1024)
	C.sc_probe_config_string(ck, (*C.char)(unsafe.Pointer(&buf[0])), C.size_t(len(buf)))
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i])
		}
	}
	return string(buf)
}

func cIntProbe(key string, def int) int {
	ck := C.CString(key)
	defer C.free(unsafe.Pointer(ck))
	return int(C.sc_probe_config_int(ck, C.int(def)))
}

func cDataDirProbe() string {
	buf := make([]byte, 1024)
	C.sc_probe_data_dir((*C.char)(unsafe.Pointer(&buf[0])), C.size_t(len(buf)))
	for i, b := range buf {
		if b == 0 {
			return string(buf[:i])
		}
	}
	return string(buf)
}
