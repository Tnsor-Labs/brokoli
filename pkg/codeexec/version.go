package codeexec

import (
	"fmt"
	"regexp"
	"strconv"
)

// wrapperVersion is parsed from the embedded version.py at init, so the
// Go side and the Python side literally cannot disagree about which
// contract version ships (ADR-029). A parse failure is a build defect,
// caught by TestWrapperVersionParses before it could ever panic here.
var wrapperVersion = mustParseWrapperVersion()
var jsWrapperVersion = mustParseJSWrapperVersion()

// WrapperVersion is the code-node wrapper contract version recorded in
// every execution's audit line.
func WrapperVersion() int { return wrapperVersion }

// JSWrapperVersion is the independently versioned JavaScript wrapper
// contract embedded in this binary.
func JSWrapperVersion() int { return jsWrapperVersion }

var versionRe = regexp.MustCompile(`(?m)^WRAPPER_VERSION\s*=\s*(\d+)\s*$`)
var jsVersionRe = regexp.MustCompile(`(?m)^export const JS_WRAPPER_VERSION\s*=\s*(\d+);?\s*$`)

func mustParseWrapperVersion() int {
	raw, err := pywrapperFS.ReadFile("pywrapper/version.py")
	if err != nil {
		panic(fmt.Sprintf("codeexec: embedded version.py unreadable: %v", err))
	}
	m := versionRe.FindSubmatch(raw)
	if m == nil {
		panic("codeexec: WRAPPER_VERSION not found in embedded version.py")
	}
	v, err := strconv.Atoi(string(m[1]))
	if err != nil || v < 2 {
		panic(fmt.Sprintf("codeexec: implausible WRAPPER_VERSION %q", m[1]))
	}
	return v
}

func mustParseJSWrapperVersion() int {
	raw, err := jswrapperFS.ReadFile("jswrapper/version.mjs")
	if err != nil {
		panic(fmt.Sprintf("codeexec: embedded version.mjs unreadable: %v", err))
	}
	m := jsVersionRe.FindSubmatch(raw)
	if m == nil {
		panic("codeexec: JS_WRAPPER_VERSION not found in embedded version.mjs")
	}
	v, err := strconv.Atoi(string(m[1]))
	if err != nil || v < 1 {
		panic(fmt.Sprintf("codeexec: implausible JS_WRAPPER_VERSION %q", m[1]))
	}
	return v
}
