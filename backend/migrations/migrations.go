// Package migrations 以 go:embed 方式内嵌各数据库方言的 SQL 迁移脚本。
//
// 目录约定：
//
//	migrations/<dialect>/<NNNN>_<name>.sql
//
// NNNN 为递增的迁移编号，同一编号在不同方言目录下语义必须一致。
// 迁移文件一旦发布即为不可变：内容变更会导致 checksum 校验失败（见 adapter.Migrate）。
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed sqlite/*.sql tsdb/*.sql
var FS embed.FS

// DialectFS 返回指定方言的迁移文件集合。
// 方言尚未提供迁移目录时返回错误，由调用方决定是否属于致命问题。
func DialectFS(dialect string) (fs.FS, error) {
	return fs.Sub(FS, dialect)
}

// TSDBFS 返回内嵌时序存储的迁移文件集合。
// 时序表独立于元数据方言：元数据可切到外部 MySQL / PostgreSQL，时序仍保持内嵌，
// 因此不能把时序表混进 sqlite/ 目录（否则元数据切走后这些表会被漏建或误建）。
func TSDBFS() (fs.FS, error) {
	return fs.Sub(FS, "tsdb")
}
