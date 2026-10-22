//go:build !unix

package tools

import "os"

const backgroundTrackingSupported = false

func lockOrphanLedger(*os.File) error          { return nil }
func unlockOrphanLedger(*os.File)              {}
func processAlive(int) bool                    { return false }
func processGroupAlive(int) bool               { return false }
func killOrphanProcessGroup(orphanRecord) bool { return false }
