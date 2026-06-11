// Copyright (c) 2026, go-filesystems
// SPDX-License-Identifier: BSD-3-Clause

package ufs

import (
	"errors"
	"reflect"
	"testing"
)

func TestSplitPath(t *testing.T) {
	cases := []struct {
		in   string
		want []string
		err  bool
	}{
		{"/", nil, false},
		{"", nil, true},
		{"foo", nil, true},
		{"/boot", []string{"boot"}, false},
		{"/boot/", []string{"boot"}, false},
		{"//boot///kernel/kernel", []string{"boot", "kernel", "kernel"}, false},
		{"/./boot", []string{"boot"}, false},
		{"/a/./b/../c", []string{"a", "c"}, false},
	}
	for _, c := range cases {
		got, err := splitPath(c.in)
		if c.err {
			if err == nil {
				t.Errorf("splitPath(%q) expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitPath(%q) unexpected err: %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitPath(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSplitPath_DoubleSlash(t *testing.T) {
	if _, err := splitPath("/foo//bar"); err != nil {
		t.Errorf("path.Clean should have normalised //: got %v", err)
	}
}

func TestSplitPath_RejectsEmpty(t *testing.T) {
	if _, err := splitPath(""); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("err = %v, want ErrInvalidPath", err)
	}
}
