// SPDX-License-Identifier: AGPL-3.0-only

//go:build !linux

package execx

// The tests that need these skip outside Linux; nothing is ever signalled.
func procStart(int) string { return "" }

func killOwn(int, string) {}
