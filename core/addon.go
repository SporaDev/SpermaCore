package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// artifactExt returns shared library file extension for current os.
func artifactExt() string {
	switch runtime.GOOS {
	case "windows":
		return ".dll"
	case "darwin":
		return ".dylib"
	default:
		return ".so"
	}
}

// resolveModuleDir converts [plugins].load entry to module directory path.
func resolveModuleDir(rootDir, entry string) (name, dir string) {
	name = filepath.Base(strings.TrimRight(entry, `/\`))
	dir = filepath.Join(rootDir, "modules", name)
	return
}

// findBuiltArtifact searches for compiled shared library in module build directory.
// A module may be built by different toolchains into different subdirectories
// (e.g. build/telegram.dll from the root CMake build and build/cmake/telegram.dll
// from the per-module build), so the newest artifact wins instead of the first
// path found during the walk.
func findBuiltArtifact(moduleDir, name string) string {
	buildDir := filepath.Join(moduleDir, "build")
	ext := artifactExt()
	var found string
	var foundMod time.Time
	_ = filepath.WalkDir(buildDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if d.Name() != name+ext {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			if found == "" {
				found = path
			}
			return nil
		}
		if found == "" || info.ModTime().After(foundMod) {
			found = path
			foundMod = info.ModTime()
		}
		return nil
	})
	return found
}

// newestSourceMtime returns latest source modification timestamp for rebuild detection.
func newestSourceMtime(moduleDir, rootDir string) int64 {
	best := int64(-1)
	check := func(p string) {
		if info, err := os.Stat(p); err == nil {
			if m := info.ModTime().UnixNano(); m > best {
				best = m
			}
		}
	}
	check(filepath.Join(rootDir, "include", "plugin_api.h"))
	check(filepath.Join(moduleDir, "CMakeLists.txt"))
	srcDir := filepath.Join(moduleDir, "src")
	if _, err := os.Stat(srcDir); err == nil {
		_ = filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() {
				return nil
			}
			name := d.Name()
			if strings.HasSuffix(name, ".c") || strings.HasSuffix(name, ".cpp") ||
				strings.HasSuffix(name, ".cc") || strings.HasSuffix(name, ".cxx") ||
				strings.HasSuffix(name, ".h") || strings.HasSuffix(name, ".hpp") {
				check(path)
			}
			return nil
		})
	}
	return best
}

// artifactIsStale checks if artifact is older than its dependencies.
func artifactIsStale(artifact, moduleDir, rootDir string) bool {
	srcDir := filepath.Join(moduleDir, "src")
	if _, err := os.Stat(srcDir); err != nil {
		// no src directory, binary-only distribution: artifact cannot be rebuilt
		return false
	}
	if info, err := os.Stat(artifact); err != nil {
		return false
	} else {
		return info.ModTime().UnixNano() < newestSourceMtime(moduleDir, rootDir)
	}
}

// cleanAllModuleBuilds purges build dirs for -rebuild flag.
func cleanAllModuleBuilds(rootDir string, log *Logger) {
	modulesDir := filepath.Join(rootDir, "modules")
	entries, err := os.ReadDir(modulesDir)
	if err != nil {
		log.Warn("rebuild: cannot read dir %s: %v", modulesDir, err)
		return
	}

	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		buildDir := filepath.Join(modulesDir, e.Name(), "build")
		if _, err := os.Stat(buildDir); err != nil {
			continue // no build dir
		}
		if err := os.RemoveAll(buildDir); err != nil {
			log.Warn("rebuild: cannot remove dir %s: %v", buildDir, err)
			continue
		}
		log.Info("rebuild: cleaned dir %s", buildDir)
		removed++
	}
	log.Info("rebuild: cleaned %d module build dirs", removed)
}

// writeGeneratedCMakeLists writes fallback CMakeLists.txt for module.
func writeGeneratedCMakeLists(buildDir, name, srcDir, includeDir string) error {
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return err
	}
	norm := func(p string) string { return strings.ReplaceAll(p, `\`, `/`) }
	content := fmt.Sprintf(`cmake_minimum_required(VERSION 3.20)
project(%[1]s C CXX)

set(CMAKE_C_STANDARD 11)
set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_POSITION_INDEPENDENT_CODE ON)

file(GLOB %[1]s_SOURCES "%[2]s/*.c" "%[2]s/*.cpp")
add_library(%[1]s SHARED ${%[1]s_SOURCES})
target_include_directories(%[1]s PRIVATE "%[3]s")
set_target_properties(%[1]s PROPERTIES PREFIX "")

if(MSVC)
    target_compile_options(%[1]s PRIVATE /W4 /WX)
else()
    target_compile_options(%[1]s PRIVATE -Wall -Wextra -Werror
        $<$<COMPILE_LANGUAGE:CXX>:-Wno-missing-field-initializers>)
endif()

if(WIN32)
    target_link_libraries(%[1]s ws2_32)
    if("%[1]s" STREQUAL "websearch")
        target_link_libraries(%[1]s winhttp)
    endif()
endif()
`, name, norm(srcDir), norm(includeDir))
	return os.WriteFile(filepath.Join(buildDir, "CMakeLists.txt"), []byte(content), 0o644)
}

// pickGenerator selects preferred cmake generator.
func pickGenerator() []string {
	if _, err := exec.LookPath("ninja"); err == nil {
		return []string{"-G", "Ninja"}
	}
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("mingw32-make"); err == nil {
			return []string{"-G", "MinGW Makefiles"}
		}
	}
	return nil
}

// buildModule compiles plugin using cmake.
func buildModule(moduleDir, name, rootDir string, log *Logger) error {
	if abs, err := filepath.Abs(moduleDir); err == nil {
		moduleDir = abs
	}
	if abs, err := filepath.Abs(rootDir); err == nil {
		rootDir = abs
	}
	buildDir := filepath.Join(moduleDir, "build")
	cmakeBuildDir := filepath.Join(buildDir, "cmake")
	includeDir := filepath.Join(rootDir, "include")

	// use custom CMakeLists.txt if provided, else generated fallback
	ownCMake := filepath.Join(moduleDir, "CMakeLists.txt")
	var cfgSrc string
	if _, err := os.Stat(ownCMake); err != nil {
		srcDir := filepath.Join(moduleDir, "src")
		if err := writeGeneratedCMakeLists(buildDir, name, srcDir, includeDir); err != nil {
			return fmt.Errorf("write CMakeLists: %w", err)
		}
		cfgSrc = buildDir
		log.Info("cmake configure: %s (generated cmakelists.txt)", name)
	} else {
		cfgSrc = moduleDir
		log.Info("cmake configure: %s (module cmakelists.txt)", name)
	}

	cfgArgs := []string{"-S", cfgSrc, "-B", cmakeBuildDir}
	cfgArgs = append(cfgArgs, pickGenerator()...)
	if _, err := exec.LookPath("gcc"); err == nil {
		cfgArgs = append(cfgArgs,
			"-DCMAKE_C_COMPILER=gcc",
			"-DCMAKE_CXX_COMPILER=g++",
		)
	}
	var cfgOut bytes.Buffer
	cfgCmd := exec.Command("cmake", cfgArgs...)
	cfgCmd.Stdout = &cfgOut
	cfgCmd.Stderr = &cfgOut
	if err := cfgCmd.Run(); err != nil {
		return fmt.Errorf("cmake configure failed:\n%s", cfgOut.String())
	}

	log.Info("cmake build: %s", name)
	buildArgs := []string{"--build", cmakeBuildDir, "--config", "Release"}
	var buildOut bytes.Buffer
	buildCmd := exec.Command("cmake", buildArgs...)
	buildCmd.Stdout = &buildOut
	buildCmd.Stderr = &buildOut
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("cmake build failed:\n%s", buildOut.String())
	}
	return nil
}
