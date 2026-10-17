package cli

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/benenen/ah/internal/transfer"
	"github.com/pkg/sftp"
	"github.com/spf13/cobra"
)

func (a *app) backupCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "backup", Aliases: []string{"b"}, Short: "List and restore backups made by cp --backup",
		Long: `列出和还原 cp --backup 留下的备份。

备份与原文件同目录，名为 bak.NAME.bak-UTC时间戳。TARGET 写原文件路径（NAME:PATH 为远端，否则为本地），不是备份文件本身。`,
	}
	completeTarget := func(cmd *cobra.Command, args []string, partial string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return a.completeRemote(cmd, args, partial)
	}
	ls := &cobra.Command{
		Use: "ls TARGET", Short: "List a file's backups, newest first", Args: cobra.ExactArgs(1), ValidArgsFunction: completeTarget,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, target, display, closeTarget, err := a.openBackupTarget(cmd, args[0])
			if err != nil {
				return err
			}
			defer closeTarget()
			_, backups, err := transfer.ListBackups(cmd.Context(), client, target)
			if err != nil {
				return err
			}
			if len(backups) == 0 {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "No backups of %s\n", display)
				return err
			}
			out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			if _, err := fmt.Fprintln(out, "#\tTAKEN (UTC)\tSIZE\tBACKUP"); err != nil {
				return err
			}
			for i, b := range backups {
				if _, err := fmt.Fprintf(out, "%d\t%s\t%s\t%s\n", i+1, b.Time.Format("2006-01-02 15:04:05"), humanBytes(b.Size), backupBase(client, b.Path)); err != nil {
					return err
				}
			}
			return out.Flush()
		},
	}
	var keep int
	restore := &cobra.Command{
		Use:   "restore TARGET [VERSION]",
		Short: "Restore a file from a backup (default: the newest)",
		Long: `把 TARGET 还原为某个备份的内容，原子替换。

VERSION 省略时用最新备份；可写 backup ls 中的序号（1 为最新），或至少含 8 位日期的时间戳前缀（如 20260930、20260930T1234），也可写完整备份文件名。
还原前先把当前文件备份一份，所以还原本身也能再还原回来；备份数同 cp --backup 受 --backup-keep 限制，被还原的那份不会被清理。
当前内容已与所选备份相同时不做任何改动。`,
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeTarget,
		RunE: func(cmd *cobra.Command, args []string) error {
			if keep < 1 || keep > 1000 {
				return fmt.Errorf("--backup-keep must be between 1 and 1000")
			}
			client, target, display, closeTarget, err := a.openBackupTarget(cmd, args[0])
			if err != nil {
				return err
			}
			defer closeTarget()
			_, backups, err := transfer.ListBackups(cmd.Context(), client, target)
			if err != nil {
				return err
			}
			version := ""
			if len(args) == 2 {
				version = args[1]
			}
			chosen, err := selectBackup(client, backups, version)
			if err != nil {
				return fmt.Errorf("%s: %w", display, err)
			}
			result, err := transfer.Restore(cmd.Context(), client, target, chosen.Path, keep)
			if err != nil {
				return err
			}
			from := backupBase(client, chosen.Path)
			switch {
			case result.Unchanged:
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s already matches %s; nothing changed\n", display, from)
			case result.SavedAs != "":
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Restored %s from %s (replaced version saved as %s)\n", display, from, backupBase(client, result.SavedAs))
			default:
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Restored %s from %s\n", display, from)
			}
			return err
		},
	}
	restore.Flags().IntVar(&keep, "backup-keep", 5, "keep at most this many backups per file when saving the replaced version")
	cmd.AddCommand(ls, restore)
	return cmd
}

// openBackupTarget resolves a cp-style endpoint and opens its filesystem; a
// local endpoint yields a nil client. display is how the user wrote it.
func (a *app) openBackupTarget(cmd *cobra.Command, value string) (client *sftp.Client, target, display string, closeTarget func(), err error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, "", "", nil, err
	}
	e, err := resolveCopyEndpoint(value, cwd)
	if err != nil {
		return nil, "", "", nil, err
	}
	client, closeTarget, err = a.openCopyEndpoint(cmd, e, cwd)
	if err != nil {
		return nil, "", "", nil, err
	}
	return client, e.Path, value, closeTarget, nil
}

// selectBackup picks from backups (newest first) by 1-based index, timestamp
// prefix or file name; an empty version means the newest.
func selectBackup(client *sftp.Client, backups []transfer.Backup, version string) (transfer.Backup, error) {
	if len(backups) == 0 {
		return transfer.Backup{}, fmt.Errorf("no backups to restore from")
	}
	if version == "" {
		return backups[0], nil
	}
	// Timestamps start with an 8-digit date, so shorter numbers are indexes.
	if n, err := strconv.Atoi(version); err == nil && len(version) < 8 {
		if n < 1 || n > len(backups) {
			return transfer.Backup{}, fmt.Errorf("backup #%d does not exist; there are %d (a timestamp needs at least the YYYYMMDD date)", n, len(backups))
		}
		return backups[n-1], nil
	}
	var matches []transfer.Backup
	for _, b := range backups {
		name := backupBase(client, b.Path)
		// The timestamp follows the last .bak-; the file name may contain one too.
		stamp := name[strings.LastIndex(name, ".bak-")+len(".bak-"):]
		if name == version || strings.HasPrefix(stamp, version) {
			matches = append(matches, b)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return transfer.Backup{}, fmt.Errorf("no backup matches %q (see backup ls)", version)
	}
	return transfer.Backup{}, fmt.Errorf("%q matches %d backups; use a longer timestamp or the number from backup ls", version, len(matches))
}

func backupBase(client *sftp.Client, p string) string {
	if client != nil {
		return path.Base(p)
	}
	return filepath.Base(p)
}
