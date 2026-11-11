package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestGeneratedCMakeBuildsCppModule tests compiling a C++ module via generated CMakeLists.
func TestGeneratedCMakeBuildsCppModule(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cmake build in -short mode")
	}
	if _, err := exec.LookPath("cmake"); err != nil {
		t.Skip("cmake not installed")
	}

	rootDir, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	moduleDir := t.TempDir()
	srcDir := filepath.Join(moduleDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}

	const source = `#include "plugin_api.h"

namespace {

const sc_plugin_info_t* get_info() {
    static const sc_plugin_info_t info = {
        .name = "cppprobe",
        .version = "1.0.0",
        .description = "C++ build probe",
    };
    return &info;
}

sc_status_t init(const sc_core_api_t* core, const char* cfg) {
    (void)core;
    (void)cfg;
    return SC_OK;
}

} // namespace

SC_EXPORT const sc_plugin_api_t* SC_CALL plugin_get_api(void) {
    static sc_plugin_api_t api = {};
    api.abi_major = SC_ABI_MAJOR;
    api.abi_minor = SC_ABI_MINOR;
    api.struct_size = sizeof(sc_plugin_api_t);
    api.get_info = get_info;
    api.init = init;
    return &api;
}
`
	if err := os.WriteFile(filepath.Join(srcDir, "cppprobe.cpp"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := buildModule(moduleDir, "cppprobe", rootDir, GetLogger("test.addon")); err != nil {
		t.Fatalf("buildModule: %v", err)
	}

	libName := "libcppprobe.so"
	if runtime.GOOS == "windows" {
		libName = "cppprobe.dll"
	} else if runtime.GOOS == "darwin" {
		libName = "libcppprobe.dylib"
	}
	artifact := filepath.Join(moduleDir, "build", "cmake", libName)
	if _, err := os.Stat(artifact); err != nil {
		// search fallback for platform artifact location
		found := findBuiltArtifact(moduleDir, "cppprobe")
		if found == "" {
			t.Fatalf("artifact not found after build: %v", err)
		}
		artifact = found
	}
	info, err := os.Stat(artifact)
	if err != nil {
		t.Fatalf("stat artifact: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("artifact %s is empty", artifact)
	}
}
