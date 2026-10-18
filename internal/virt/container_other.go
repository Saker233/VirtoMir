//go:build !linux

package virt

import "errors"

func shiftIDs(string, int) error { return errors.New("system containers need a Linux host") }

func containerPower(int32, bool) error { return errors.New("system containers need a Linux host") }
