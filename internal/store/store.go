package store

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Store struct {
	DB  *gorm.DB
	sql *sql.DB
}

func Open(ctx context.Context, sqlitePath string) (*Store, error) {
	if sqlitePath == "" {
		return nil, errors.New("sqlite path is required")
	}
	if err := ensureParentDir(sqlitePath); err != nil {
		return nil, err
	}

	db, err := gorm.Open(sqlite.Open(sqlitePath), &gorm.Config{
		Logger: logger.New(log.New(os.Stdout, "\r\n", log.LstdFlags), logger.Config{
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
		}),
	})
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(time.Hour)

	store := &Store{DB: db, sql: sqlDB}
	if err := store.configureSQLite(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := RunMigrations(db); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := hardenSQLiteFiles(sqlitePath); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.sql == nil {
		return nil
	}
	return s.sql.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	if s == nil || s.sql == nil {
		return errors.New("store is not initialized")
	}
	return s.sql.PingContext(ctx)
}

func (s *Store) Backup(ctx context.Context, destination string) error {
	if s == nil || s.sql == nil {
		return errors.New("store is not initialized")
	}
	if destination == "" {
		return errors.New("backup destination is required")
	}
	if err := ensureParentDir(destination); err != nil {
		return err
	}
	if _, err := os.Stat(destination); err == nil {
		return errors.New("backup destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, err := s.sql.ExecContext(ctx, "VACUUM INTO ?", destination)
	if err == nil {
		err = hardenSQLiteFiles(destination)
	}
	return err
}

func (s *Store) Optimize(ctx context.Context) error {
	if s == nil || s.sql == nil {
		return errors.New("store is not initialized")
	}
	_, err := s.sql.ExecContext(ctx, "PRAGMA optimize")
	return err
}

func (s *Store) configureSQLite(ctx context.Context) error {
	pragmas := []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA busy_timeout = 5000",
	}
	for _, pragma := range pragmas {
		if _, err := s.sql.ExecContext(ctx, pragma); err != nil {
			return err
		}
	}
	return nil
}

func ensureParentDir(path string) error {
	if isMemoryDSN(path) {
		return nil
	}
	fsPath := sqliteFilesystemPath(path)
	if fsPath == "" {
		return nil
	}
	dir := filepath.Dir(fsPath)
	if dir == "." || dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}

func isMemoryDSN(path string) bool {
	return path == ":memory:" || strings.HasPrefix(path, "file::memory:") || strings.Contains(path, "mode=memory")
}

func hardenSQLiteFiles(path string) error {
	fsPath := sqliteFilesystemPath(path)
	if fsPath == "" || isMemoryDSN(path) {
		return nil
	}
	for _, candidate := range []string{fsPath, fsPath + "-wal", fsPath + "-shm"} {
		if err := os.Chmod(candidate, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func sqliteFilesystemPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || isMemoryDSN(path) {
		return ""
	}
	if strings.HasPrefix(path, "file:") {
		path = strings.TrimPrefix(path, "file:")
		if index := strings.Index(path, "?"); index >= 0 {
			path = path[:index]
		}
	}
	if strings.HasPrefix(path, ":") {
		return ""
	}
	return path
}
