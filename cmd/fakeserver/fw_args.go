package main

import (
	"strconv"
	"strings"
)

// Args reads a server's command line the way game servers do. An option is
// matched by name without regard to case or leading dashes, so "port"
// finds "-port", "--port", "-Port" and a bare "port". Its value is either
// the next argument ("-port 7777") or what follows "=" ("-Port=7777", as
// Unreal servers take it; "-Dname=value", as Java takes properties).
type Args struct {
	list []string
}

// NewArgs wraps a command line (without the program), for tests and for
// personalities that parse part of one again.
func NewArgs(list []string) Args { return Args{list: list} }

// List is every argument as given.
func (a Args) List() []string { return append([]string(nil), a.list...) }

// bare is an argument without its leading dashes.
func bare(arg string) string { return strings.TrimLeft(arg, "-") }

// find returns the index of the option and its value, if it has one.
func (a Args) find(name string) (i int, value string, ok bool) {
	for i, arg := range a.list {
		b := bare(arg)
		if strings.EqualFold(b, name) {
			if i+1 < len(a.list) {
				return i, a.list[i+1], true
			}
			return i, "", true
		}
		if k, v, found := strings.Cut(b, "="); found && strings.EqualFold(k, name) {
			return i, v, true
		}
	}
	return -1, "", false
}

// Has says whether the option is there at all ("-nogui", "-batchmode").
func (a Args) Has(name string) bool {
	_, _, ok := a.find(name)
	return ok
}

// String is the option's value, or def when it isn't there.
func (a Args) String(name, def string) string {
	if _, v, ok := a.find(name); ok {
		return v
	}
	return def
}

// Int is the option's value as a whole number, or def when it isn't there
// or isn't a number.
func (a Args) Int(name string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(a.String(name, ""))); err == nil {
		return n
	}
	return def
}

// After is the n arguments that follow the option ("-modifier raids none"
// takes two), or nil when it isn't there or has fewer after it.
func (a Args) After(name string, n int) []string {
	i, _, ok := a.find(name)
	if !ok || i+n >= len(a.list) {
		return nil
	}
	return append([]string(nil), a.list[i+1:i+1+n]...)
}

// Contains says whether any argument is exactly s, ignoring case (a jar's
// name, a main class).
func (a Args) Contains(s string) bool {
	for _, arg := range a.list {
		if strings.EqualFold(arg, s) {
			return true
		}
	}
	return false
}
