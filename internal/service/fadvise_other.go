//go:build !linux

package service

import "os"

func fadviseDontNeed(_ *os.File) {}
