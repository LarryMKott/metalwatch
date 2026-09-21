package adapter

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"
)

// migrationFile 是一个已解析的迁移脚本。
type migrationFile struct {
	version  int
	name     string
	sql      string
	checksum string
}

// Migrate 应用 dir 下的全部 SQL 迁移。整个操作幂等，可安全重复执行。
//
// 行为约定：
//   - 每个迁移在**单个事务**内执行，失败即回滚，不留半截 schema
//   - 已应用的迁移会校验 checksum；不一致直接报错，防止历史迁移被偷偷修改
//   - 版本号来自文件名前缀（0001_init.sql → 1）
func (s *Store) Migrate(ctx context.Context, fsys fs.FS, dir string) (applied int, err error) {
	// schema_version 自身不属于业务迁移，这里用方言无关的最小 DDL
	// （sqlite 的 STRICT 表在其他方言不存在，因此不加额外修饰）
	createSchemaVersion := `CREATE TABLE IF NOT EXISTS schema_version (
		version    INTEGER PRIMARY KEY,
		name       TEXT    NOT NULL,
		applied_at TEXT    NOT NULL,
		checksum   TEXT    NOT NULL
	)`
	if _, err := s.ExecContext(ctx, s.Rebind(createSchemaVersion)); err != nil {
		return 0, fmt.Errorf("创建 schema_version 失败: %w", err)
	}

	files, err := loadMigrations(fsys, dir)
	if err != nil {
		return 0, err
	}

	done, err := s.appliedMigrations(ctx)
	if err != nil {
		return 0, err
	}

	for _, m := range files {
		if prev, ok := done[m.version]; ok {
			if prev != m.checksum {
				return applied, fmt.Errorf(
					"迁移 %04d_%s 的 checksum 与已应用版本不一致（已应用 %s，当前 %s）："+
						"已发布的迁移不可修改，请改用新增迁移文件",
					m.version, m.name, prev[:12], m.checksum[:12])
			}
			continue
		}

		if err := s.applyOne(ctx, m); err != nil {
			return applied, err
		}
		applied++
	}
	return applied, nil
}

// applyOne 在单事务内执行一个迁移并登记版本。
func (s *Store) applyOne(ctx context.Context, m migrationFile) error {
	tx, err := s.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("执行迁移 %04d_%s 失败: %w", m.version, m.name, err)
	}

	insert := s.Rebind(`INSERT INTO schema_version (version, name, applied_at, checksum)
	                    VALUES (?, ?, ?, ?)`)
	if _, err := tx.ExecContext(ctx, insert, m.version, m.name,
		time.Now().UTC().Format(time.RFC3339), m.checksum); err != nil {
		return fmt.Errorf("登记迁移版本 %d 失败: %w", m.version, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交迁移 %04d_%s 失败: %w", m.version, m.name, err)
	}
	return nil
}

// appliedMigrations 返回 已应用版本号 → checksum。
func (s *Store) appliedMigrations(ctx context.Context) (map[int]string, error) {
	rows, err := s.QueryContext(ctx, `SELECT version, checksum FROM schema_version`)
	if err != nil {
		return nil, fmt.Errorf("读取 schema_version 失败: %w", err)
	}
	defer rows.Close()

	out := map[int]string{}
	for rows.Next() {
		var v int
		var sum string
		if err := rows.Scan(&v, &sum); err != nil {
			return nil, err
		}
		out[v] = sum
	}
	return out, rows.Err()
}

// loadMigrations 读取并校验迁移文件。
func loadMigrations(fsys fs.FS, dir string) ([]migrationFile, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("读取迁移目录 %s 失败: %w", dir, err)
	}

	var files []migrationFile
	seen := map[int]string{}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		raw, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("读取迁移文件 %s 失败: %w", e.Name(), err)
		}

		version, name, err := parseMigrationName(e.Name())
		if err != nil {
			return nil, fmt.Errorf("迁移文件命名不合法 %s: %w", e.Name(), err)
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("迁移编号 %d 冲突: %s 与 %s", version, other, e.Name())
		}
		seen[version] = e.Name()

		sum := sha256.Sum256(raw)
		files = append(files, migrationFile{
			version:  version,
			name:     name,
			sql:      string(raw),
			checksum: hex.EncodeToString(sum[:]),
		})
	}

	sort.Slice(files, func(i, j int) bool { return files[i].version < files[j].version })
	return files, nil
}

// parseMigrationName 解析 "0001_init.sql" → (1, "init")。
func parseMigrationName(filename string) (int, string, error) {
	base := strings.TrimSuffix(filename, ".sql")
	idx := strings.IndexByte(base, '_')
	if idx <= 0 {
		return 0, "", fmt.Errorf("期望 <NNNN>_<name>.sql 格式")
	}
	version, err := strconv.Atoi(base[:idx])
	if err != nil {
		return 0, "", fmt.Errorf("版本号 %q 不是数字", base[:idx])
	}
	if version <= 0 {
		return 0, "", fmt.Errorf("版本号必须为正数")
	}
	return version, base[idx+1:], nil
}

// CurrentVersion 返回已应用的最大迁移版本；空库返回 0。
func (s *Store) CurrentVersion(ctx context.Context) (int, error) {
	var v sql.NullInt64
	err := s.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_version`).Scan(&v)
	if err != nil {
		return 0, err
	}
	return int(v.Int64), nil
}
