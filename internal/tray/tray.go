package tray

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/getlantern/systray"
	"golang.design/x/hotkey"
	"zomboidautobackup/internal/backup"
	"zomboidautobackup/internal/config"
	"zomboidautobackup/internal/dialog"
)

var intervalPresets = []int{5, 10, 15, 20, 25, 30, 35, 40, 45, 50, 55, 60}
var maxBackupPresets = []int{5, 10, 15}

const maxBackupCap = 20

type Tray struct {
	cfg *config.Config

	autoBackupItem *systray.MenuItem

	zomboidDisplay *systray.MenuItem
	zomboidChange  *systray.MenuItem
	backupDisplay  *systray.MenuItem
	backupChange   *systray.MenuItem

	intervalParent *systray.MenuItem
	intervalItems  []*systray.MenuItem
	intervalSet    *systray.MenuItem

	maxBackupParent *systray.MenuItem
	maxBackupItems  []*systray.MenuItem
	maxBackupSet    *systray.MenuItem

	backupBeforeItem *systray.MenuItem

	// Pre-allocated restore slots (Show/Hide at runtime)
	restoreManualEmpty *systray.MenuItem
	restoreAutoEmpty   *systray.MenuItem
	restoreManualItems []*systray.MenuItem
	restoreAutoItems   []*systray.MenuItem
	restoreManualSnaps []string
	restoreAutoSnaps   []string
}

func New() *Tray {
	cfg, _ := config.Load()
	cfg.EnsureBackupFolder() //nolint:errcheck
	cfg.Save()               //nolint:errcheck
	return &Tray{cfg: cfg}
}

func (t *Tray) Setup(icon []byte) {
	setIcon(icon)
	systray.SetTooltip("Project Zomboid 自动备份")

	t.buildSettingsSubmenu()

	systray.AddSeparator()

	autoLabel := "自动备份：关闭"
	if t.cfg.AutoBackup {
		autoLabel = "自动备份：开启"
	}
	t.autoBackupItem = systray.AddMenuItemCheckbox(autoLabel, "切换自动备份", t.cfg.AutoBackup)
	manualItem := systray.AddMenuItem("手动备份  "+hotkeyLabel, "立即执行一次备份")

	t.buildRestoreSubmenu()

	systray.AddSeparator()

	quitItem := systray.AddMenuItem("退出", "退出 ZomboidAutoBackup")

	go t.handleEvents(manualItem, quitItem)
	go t.autoBackupLoop()
	go t.hotkeyLoop()
}

func (t *Tray) buildSettingsSubmenu() {
	settingsItem := systray.AddMenuItem("设置", "配置备份设置")

	t.zomboidDisplay = settingsItem.AddSubMenuItem(
		fmt.Sprintf("Zomboid 文件夹：%s", t.cfg.ZomboidFolder), "")
	t.zomboidDisplay.Disable()
	t.zomboidChange = settingsItem.AddSubMenuItem("更改 Zomboid 文件夹…", "")

	sep1 := settingsItem.AddSubMenuItem("───────────────────", "")
	sep1.Disable()

	t.backupDisplay = settingsItem.AddSubMenuItem(
		fmt.Sprintf("备份文件夹：%s", t.cfg.BackupFolder), "")
	t.backupDisplay.Disable()
	t.backupChange = settingsItem.AddSubMenuItem("更改备份文件夹…", "")

	sep2 := settingsItem.AddSubMenuItem("───────────────────", "")
	sep2.Disable()

	t.intervalParent = settingsItem.AddSubMenuItem(
		fmt.Sprintf("备份间隔：%d 分钟", t.cfg.BackupInterval), "")
	for _, v := range intervalPresets {
		item := t.intervalParent.AddSubMenuItem(fmt.Sprintf("%d 分钟", v), "")
		if v == t.cfg.BackupInterval {
			item.Check()
		}
		t.intervalItems = append(t.intervalItems, item)
	}
	t.intervalSet = t.intervalParent.AddSubMenuItem("自定义…", "输入自定义备份间隔（分钟）")

	sep3 := settingsItem.AddSubMenuItem("───────────────────", "")
	sep3.Disable()

	t.maxBackupParent = settingsItem.AddSubMenuItem(
		fmt.Sprintf("最大备份数：%d", t.cfg.MaxBackupFiles), "")
	for _, v := range maxBackupPresets {
		item := t.maxBackupParent.AddSubMenuItem(fmt.Sprintf("%d", v), "")
		if v == t.cfg.MaxBackupFiles {
			item.Check()
		}
		t.maxBackupItems = append(t.maxBackupItems, item)
	}
	t.maxBackupSet = t.maxBackupParent.AddSubMenuItem("自定义…（最大 20）", "输入自定义最大备份数量")
}

func (t *Tray) buildRestoreSubmenu() {
	restoreItem := systray.AddMenuItem("恢复", "恢复已保存的备份")

	t.backupBeforeItem = restoreItem.AddSubMenuItemCheckbox(
		"恢复前备份", "恢复前备份当前 Saves 文件夹以确保安全",
		t.cfg.BackupBeforeRestore)

	sep := restoreItem.AddSubMenuItem("───────────────────", "")
	sep.Disable()

	// 手动 restore sub-submenu
	manualParent := restoreItem.AddSubMenuItem("手动", "从手动备份中恢复")
	t.restoreManualEmpty = manualParent.AddSubMenuItem("暂无手动备份", "")
	t.restoreManualEmpty.Disable()
	for i := 0; i < maxBackupCap; i++ {
		item := manualParent.AddSubMenuItem("", "")
		item.Hide()
		t.restoreManualItems = append(t.restoreManualItems, item)
	}

	// 自动 restore sub-submenu
	autoParent := restoreItem.AddSubMenuItem("自动", "从自动备份中恢复")
	t.restoreAutoEmpty = autoParent.AddSubMenuItem("暂无自动备份", "")
	t.restoreAutoEmpty.Disable()
	for i := 0; i < maxBackupCap; i++ {
		item := autoParent.AddSubMenuItem("", "")
		item.Hide()
		t.restoreAutoItems = append(t.restoreAutoItems, item)
	}

	t.refreshRestoreMenus()
}

// refreshRestoreMenus re-reads backup dirs and updates the pre-allocated slots.
func (t *Tray) refreshRestoreMenus() {
	t.refreshRestoreDir("manual", t.restoreManualItems, t.restoreManualEmpty, &t.restoreManualSnaps)
	t.refreshRestoreDir("auto", t.restoreAutoItems, t.restoreAutoEmpty, &t.restoreAutoSnaps)
}

func (t *Tray) refreshRestoreDir(subdir string, items []*systray.MenuItem, emptyItem *systray.MenuItem, snaps *[]string) {
	dir := filepath.Join(t.cfg.BackupFolder, subdir)
	newSnaps, _ := backup.ListSnapshots(dir) // newest first; nil on missing dir = empty list
	*snaps = newSnaps

	if len(newSnaps) == 0 {
		emptyItem.Show()
	} else {
		emptyItem.Hide()
	}

	for i, item := range items {
		if i < len(newSnaps) {
			item.SetTitle(newSnaps[i])
			item.Show()
		} else {
			item.Hide()
		}
	}
}

func (t *Tray) handleEvents(manualItem, quitItem *systray.MenuItem) {
	intervalCh := make(chan int, 1)
	for i, item := range t.intervalItems {
		i, item := i, item
		go func() {
			for range item.ClickedCh {
				intervalCh <- i
			}
		}()
	}

	maxBackupCh := make(chan int, 1)
	for i, item := range t.maxBackupItems {
		i, item := i, item
		go func() {
			for range item.ClickedCh {
				maxBackupCh <- i
			}
		}()
	}

	restoreManualCh := make(chan int, 1)
	for i, item := range t.restoreManualItems {
		i, item := i, item
		go func() {
			for range item.ClickedCh {
				restoreManualCh <- i
			}
		}()
	}

	restoreAutoCh := make(chan int, 1)
	for i, item := range t.restoreAutoItems {
		i, item := i, item
		go func() {
			for range item.ClickedCh {
				restoreAutoCh <- i
			}
		}()
	}

	for {
		select {
		case <-t.autoBackupItem.ClickedCh:
			t.toggleAutoBackup()
		case <-manualItem.ClickedCh:
			go func() {
				backup.Manual(t.cfg.ZomboidFolder, t.cfg.BackupFolder, t.cfg.MaxBackupFiles)
				t.refreshRestoreMenus()
			}()
		case <-t.zomboidChange.ClickedCh:
			t.changeZomboidFolder()
		case <-t.backupChange.ClickedCh:
			t.changeBackupFolder()
		case idx := <-intervalCh:
			t.selectInterval(idx)
		case <-t.intervalSet.ClickedCh:
			t.promptInterval()
		case idx := <-maxBackupCh:
			t.selectMaxBackup(idx)
		case <-t.maxBackupSet.ClickedCh:
			t.promptMaxBackups()
		case <-t.backupBeforeItem.ClickedCh:
			t.toggleBackupBefore()
		case idx := <-restoreManualCh:
			if idx < len(t.restoreManualSnaps) {
				go t.doRestore("manual", t.restoreManualSnaps[idx])
			}
		case idx := <-restoreAutoCh:
			if idx < len(t.restoreAutoSnaps) {
				go t.doRestore("auto", t.restoreAutoSnaps[idx])
			}
		case <-quitItem.ClickedCh:
			systray.Quit()
			return
		}
	}
}

func (t *Tray) doRestore(subdir, snapName string) {
	confirmed := confirm(fmt.Sprintf(
		"恢复“%s”？\n\n这将替换当前的 Saves 文件夹。", snapName))
	if !confirmed {
		return
	}
	backupPath := filepath.Join(t.cfg.BackupFolder, subdir, snapName)
	backup.Restore(backupPath, t.cfg.ZomboidFolder, t.cfg.BackupFolder, t.cfg.BackupBeforeRestore)
}

func (t *Tray) toggleAutoBackup() {
	t.cfg.AutoBackup = !t.cfg.AutoBackup
	if t.cfg.AutoBackup {
		t.autoBackupItem.SetTitle("自动备份：开启")
		t.autoBackupItem.Check()
	} else {
		t.autoBackupItem.SetTitle("自动备份：关闭")
		t.autoBackupItem.Uncheck()
	}
	t.cfg.Save() //nolint:errcheck
}

func (t *Tray) toggleBackupBefore() {
	t.cfg.BackupBeforeRestore = !t.cfg.BackupBeforeRestore
	if t.cfg.BackupBeforeRestore {
		t.backupBeforeItem.Check()
	} else {
		t.backupBeforeItem.Uncheck()
	}
	t.cfg.Save() //nolint:errcheck
}

func (t *Tray) selectInterval(idx int) {
	for i, item := range t.intervalItems {
		if i == idx {
			item.Check()
		} else {
			item.Uncheck()
		}
	}
	t.cfg.BackupInterval = intervalPresets[idx]
	t.intervalParent.SetTitle(fmt.Sprintf("备份间隔：%d 分钟", t.cfg.BackupInterval))
	t.cfg.Save() //nolint:errcheck
}

func (t *Tray) promptInterval() {
	result, ok := prompt("备份间隔（分钟，1–1440）：", strconv.Itoa(t.cfg.BackupInterval))
	if !ok {
		return
	}
	val, err := strconv.Atoi(strings.TrimSpace(result))
	if err != nil || val < 1 || val > 1440 {
		return
	}
	for _, item := range t.intervalItems {
		item.Uncheck()
	}
	for i, v := range intervalPresets {
		if v == val {
			t.intervalItems[i].Check()
			break
		}
	}
	t.cfg.BackupInterval = val
	t.intervalParent.SetTitle(fmt.Sprintf("备份间隔：%d 分钟", val))
	t.cfg.Save() //nolint:errcheck
}

func (t *Tray) selectMaxBackup(idx int) {
	for i, item := range t.maxBackupItems {
		if i == idx {
			item.Check()
		} else {
			item.Uncheck()
		}
	}
	t.cfg.MaxBackupFiles = maxBackupPresets[idx]
	t.maxBackupParent.SetTitle(fmt.Sprintf("最大备份数：%d", t.cfg.MaxBackupFiles))
	t.cfg.Save() //nolint:errcheck
}

func (t *Tray) promptMaxBackups() {
	result, ok := prompt("最大备份文件数量（1–20）：", strconv.Itoa(t.cfg.MaxBackupFiles))
	if !ok {
		return
	}
	val, err := strconv.Atoi(strings.TrimSpace(result))
	if err != nil || val < 1 {
		return
	}
	if val > maxBackupCap {
		val = maxBackupCap
	}
	for _, item := range t.maxBackupItems {
		item.Uncheck()
	}
	for i, v := range maxBackupPresets {
		if v == val {
			t.maxBackupItems[i].Check()
			break
		}
	}
	t.cfg.MaxBackupFiles = val
	t.maxBackupParent.SetTitle(fmt.Sprintf("最大备份数：%d", val))
	t.cfg.Save() //nolint:errcheck
}

func (t *Tray) changeBackupFolder() {
	path, ok := chooseFolder("选择用于存放备份的文件夹：", t.cfg.BackupFolder)
	if !ok {
		return
	}
	t.cfg.BackupFolder = path
	t.backupDisplay.SetTitle(fmt.Sprintf("备份文件夹：%s", path))
	t.cfg.Save() //nolint:errcheck
	t.refreshRestoreMenus()
}

func (t *Tray) changeZomboidFolder() {
	path, ok := chooseFolder("选择你的 Zomboid 游戏文件夹：", t.cfg.ZomboidFolder)
	if !ok {
		return
	}
	t.cfg.ZomboidFolder = path
	t.zomboidDisplay.SetTitle(fmt.Sprintf("Zomboid 文件夹：%s", path))
	t.cfg.Save() //nolint:errcheck
}

func (t *Tray) hotkeyLoop() {
	hk := hotkey.New(hotkeyModifiers, hotkey.KeyB)
	if err := hk.Register(); err != nil {
		return // silently skip if hotkey can't be registered (e.g. already taken)
	}
	defer hk.Unregister()
	for range hk.Keydown() {
		go func() {
			backup.Manual(t.cfg.ZomboidFolder, t.cfg.BackupFolder, t.cfg.MaxBackupFiles)
			t.refreshRestoreMenus()
		}()
	}
}

func (t *Tray) autoBackupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	var lastRun time.Time
	for range ticker.C {
		if !t.cfg.AutoBackup {
			continue
		}
		interval := time.Duration(t.cfg.BackupInterval) * time.Minute
		if time.Since(lastRun) >= interval {
			lastRun = time.Now()
			go func() {
				backup.Auto(t.cfg.ZomboidFolder, t.cfg.BackupFolder, t.cfg.MaxBackupFiles)
				t.refreshRestoreMenus()
			}()
		}
	}
}

func chooseFolder(prompt, defaultPath string) (string, bool) {
	return dialog.ChooseFolder(prompt, defaultPath)
}

func confirm(message string) bool {
	return dialog.Confirm(message, "恢复")
}

func prompt(message, defaultValue string) (string, bool) {
	return dialog.Prompt(message, defaultValue)
}
