package main

import (
	"testing"
)

func TestHeadlessCheck(t *testing.T) {
	if err := HeadlessCheck(nil, 1); err != nil {
		t.Errorf("unexpected %v", err)
	}
	if err := HeadlessCheck(nil, 0); err == nil {
		t.Error("expected an error without sync folder")
	}
	if err := HeadlessCheck([]string{"f"}, 1); err == nil {
		t.Error("expected an error with files")
	}
}
