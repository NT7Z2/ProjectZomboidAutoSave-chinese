package backup

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"zomboidautobackup/internal/dialog"
)

// Manual triggers a one-off backup into {backupFolder}/manual/.
func Manual(zomboidFolder, backupFolder string, maxBackups int) {
	run(zomboidFolder, backupFolder, "manual", maxBackups, true)
}

// Auto triggers a scheduled backup into {backupFolder}/auto/.
// Silent on success — no notification so it doesn't interrupt the user.
func Auto(zomboidFolder, backupFolder string, maxBackups int) {
	run(zomboidFolder, backupFolder, "auto", maxBackups, false)
}

// Restore unpacks backupPath into zomboidFolder/Saves.
// If backupFirst is true, the current Saves folder is archived to
// {backupFolder}/before-restore/ before anything is touched.
func Restore(backupPath, zomboidFolder, backupFolder string, backupFirst bool) {
	savesDir := filepath.Join(zomboidFolder, "Saves")

	if backupFirst {
		if _, err := os.Stat(savesDir); err == nil {
			beforeDir := filepath.Join(backupFolder, "before-restore")
			if err := os.MkdirAll(beforeDir, 0o755); err == nil {
				ts := time.Now().Format("2006-01-02_15-04-05")
				snapPath := filepath.Join(beforeDir, fmt.Sprintf("r-%s.tar.zst", ts))
				// Best-effort — don't abort the restore if this fails
				tarZst(savesDir, snapPath) //nolint:errcheck
			}
		}
	}

	// Remove existing Saves so the extracted tree replaces it cleanly
	if err := os.RemoveAll(savesDir); err != nil {
		showDialog(fmt.Sprintf("无法删除现有的 Saves 文件夹：\n%v", err))
		return
	}

	if err := untarZst(backupPath, zomboidFolder); err != nil {
		showDialog(fmt.Sprintf("恢复失败：\n%v", err))
		return
	}

	showNotification("Zomboid 备份", "恢复成功！")
}

// ListSnapshots returns snapshot file names in dir, newest first.
func ListSnapshots(dir string) ([]string, error) {
	files, err := readSnapshotFiles(dir)
	if err != nil {
		return nil, err
	}

	sort.Slice(files, func(i, j int) bool {
		if files[i].created.Equal(files[j].created) {
			return files[i].name > files[j].name
		}
		return files[i].created.After(files[j].created)
	})

	snaps := make([]string, 0, len(files))
	for _, file := range files {
		snaps = append(snaps, file.name)
	}
	return snaps, nil
}

// run is the shared implementation for both manual and auto backups.
func run(zomboidFolder, backupFolder, subdir string, maxBackups int, notifySuccess bool) {
	savesDir := filepath.Join(zomboidFolder, "Saves")

	if _, err := os.Stat(savesDir); os.IsNotExist(err) {
		if notifySuccess {
			showDialog("在配置的 Zomboid 文件夹中未找到 “Saves” 文件夹。\n\n请检查 Zomboid 文件夹设置。")
		}
		return
	}

	destDir := filepath.Join(backupFolder, subdir)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		showDialog(fmt.Sprintf("无法创建备份文件夹：\n%v", err))
		return
	}

	timestamp := time.Now().Format("2006-01-02_15-04-05")
	prefix := string([]rune(subdir)[0]) // "m" for manual, "a" for auto
	snapName := fmt.Sprintf("%s-%s.tar.zst", prefix, timestamp)
	snapPath := filepath.Join(destDir, snapName)

	if err := tarZst(savesDir, snapPath); err != nil {
		os.Remove(snapPath)
		showDialog(fmt.Sprintf("备份失败：\n%v", err))
		return
	}

	if err := enforceLimit(destDir, maxBackups); err != nil {
		showNotification("Zomboid 备份", fmt.Sprintf("备份已保存，但清理旧备份失败：%v", err))
		return
	}

	if notifySuccess {
		showNotification("Zomboid 备份", fmt.Sprintf("手动备份已保存：%s", snapName))
	}
}

// enforceLimit deletes the oldest snapshots in dir until at most maxBackups remain.
func enforceLimit(dir string, maxBackups int) error {
	snaps, err := readSnapshotFiles(dir)
	if err != nil {
		return err
	}

	sort.Slice(snaps, func(i, j int) bool {
		if snaps[i].created.Equal(snaps[j].created) {
			return snaps[i].name < snaps[j].name
		}
		return snaps[i].created.Before(snaps[j].created)
	})

	for len(snaps) > maxBackups {
		oldest := filepath.Join(dir, snaps[0].name)
		if err := os.Remove(oldest); err != nil {
			return fmt.Errorf("removing %s: %w", snaps[0].name, err)
		}
		snaps = snaps[1:]
	}
	return nil
}

type snapshotFile struct {
	name    string
	created time.Time
}

func readSnapshotFiles(dir string) ([]snapshotFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var snaps []snapshotFile
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".zst" {
			info, err := e.Info()
			if err != nil {
				return nil, err
			}
			snaps = append(snaps, snapshotFile{
				name:    e.Name(),
				created: snapshotCreatedAt(e.Name(), info.ModTime()),
			})
		}
	}
	return snaps, nil
}

func snapshotCreatedAt(name string, fallback time.Time) time.Time {
	const layout = "2006-01-02_15-04-05"

	base := strings.TrimSuffix(name, ".tar.zst")
	if len(base) < len(layout) {
		return fallback
	}

	ts := base[len(base)-len(layout):]
	created, err := time.ParseInLocation(layout, ts, time.Local)
	if err != nil {
		return fallback
	}
	return created
}

// tarZst archives sourceDir into a zstd-compressed tar at destPath.
func tarZst(sourceDir, destPath string) error {
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()

	zw, err := zstd.NewWriter(f, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return err
	}
	defer zw.Close()

	tw := tar.NewWriter(zw)
	defer tw.Close()

	baseDir := filepath.Dir(sourceDir)

	return filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(baseDir, path)
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = relPath
		if info.IsDir() {
			header.Name += "/"
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()
		_, err = io.Copy(tw, src)
		return err
	})
}

// untarZst extracts a .tar.zst archive into destDir.
func untarZst(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	zr, err := zstd.NewReader(f)
	if err != nil {
		return err
	}
	defer zr.Close()

	tr := tar.NewReader(zr)
	cleanDest := filepath.Clean(destDir)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target, err := safeExtractPath(cleanDest, header.Name)
		if err != nil {
			return err
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			out.Close()
			if copyErr != nil {
				return copyErr
			}
		}
	}
	return nil
}

func safeExtractPath(cleanDest, name string) (string, error) {
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("archive contains absolute path %q", name)
	}

	target := filepath.Clean(filepath.Join(cleanDest, name))
	if target != cleanDest && !strings.HasPrefix(target, cleanDest+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive path escapes destination: %q", name)
	}
	return target, nil
}

func showDialog(message string) {
	dialog.Alert(message)
}

func showNotification(title, message string) {
	dialog.Notify(title, message)
}
