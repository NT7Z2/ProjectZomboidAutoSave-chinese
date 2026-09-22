//go:build windows

package dialog

import "github.com/ncruces/zenity"

func ChooseFolder(prompt, defaultPath string) (string, bool) {
	path, err := zenity.SelectFile(
		zenity.Title(prompt),
		zenity.Filename(defaultPath),
		zenity.Directory(),
	)
	return path, err == nil
}

func Prompt(message, defaultValue string) (string, bool) {
	result, err := zenity.Entry(message,
		zenity.Title("ZomboidAutoBackup"),
		zenity.EntryText(defaultValue),
	)
	return result, err == nil
}

func Confirm(message, okLabel string) bool {
	err := zenity.Question(message,
		zenity.Title("ZomboidAutoBackup"),
		zenity.OKLabel(okLabel),
		zenity.CancelLabel("取消"),
		zenity.WarningIcon,
	)
	return err == nil
}

func Alert(message string) {
	zenity.Error(message, zenity.Title("ZomboidAutoBackup")) //nolint:errcheck
}

func Notify(title, message string) {
	zenity.Notify(message, zenity.Title(title)) //nolint:errcheck
}
