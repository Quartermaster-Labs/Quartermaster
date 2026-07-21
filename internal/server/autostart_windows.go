//go:build windows

package server

import (
	"errors"

	"golang.org/x/sys/windows/registry"
)

// Per-user Run key: no admin rights needed, which matches the per-user Inno
// installer. HKLM would require elevation and would also autostart for other
// Windows accounts, which is not what "start with the system" means here.
const autostartRunKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func autostartSupported() bool { return true }

func autostartRead() (string, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartRunKey, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	defer k.Close()
	v, _, err := k.GetStringValue(autostartValueName)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return v, nil
}

func autostartWrite(cmd string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, autostartRunKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetStringValue(autostartValueName, cmd)
}

func autostartClear() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartRunKey, registry.SET_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(autostartValueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}
