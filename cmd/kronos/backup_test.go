package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// setupTempDataDir redirects platform.DataDir() to a fresh temp dir — same
// pattern used by internal/hooks tests. Skips on macOS (fixed ~/Library
// path, no env override there).
func setupTempDataDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("macOS DataDir usa ~/Library/Application Support — no overrideable por env")
	}
	base := t.TempDir()
	kronosDir := filepath.Join(base, "kronos")
	if err := os.MkdirAll(kronosDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	switch runtime.GOOS {
	case "windows":
		t.Setenv("LOCALAPPDATA", base)
	default:
		t.Setenv("XDG_DATA_HOME", base)
	}
	return kronosDir
}

// makeTestDB crea un kronos.db real (mismas migraciones que produce
// store.New) con una fila de prueba, para poder verificar que un backup/
// restore preserva datos reales, no solo copia bytes.
func makeTestDB(t *testing.T, path string) {
	t.Helper()
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE marker (id INTEGER PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO marker(value) VALUES ('original')`); err != nil {
		t.Fatal(err)
	}
}

func readMarker(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v string
	if err := db.QueryRow(`SELECT value FROM marker LIMIT 1`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRunBackupOnce_CreatesSQLiteBackupWithRealData(t *testing.T) {
	dataDir := setupTempDataDir(t)
	dbPath := filepath.Join(dataDir, "kronos.db")
	makeTestDB(t, dbPath)

	if err := runBackupOnce(); err != nil {
		t.Fatalf("runBackupOnce: %v", err)
	}

	dir := filepath.Join(dataDir, "backups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("leer backups dir: %v", err)
	}
	var found string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".db" {
			found = filepath.Join(dir, e.Name())
		}
	}
	if found == "" {
		t.Fatal("no se creó ningún backup .db")
	}
	if got := readMarker(t, found); got != "original" {
		t.Errorf("el backup no tiene los datos reales: got %q", got)
	}
}

func TestRunBackupOnce_PostgresConfiguredButNoPgDump_DoesNotFailCommand(t *testing.T) {
	setupTempDataDir(t)
	dbPath, _ := os.CreateTemp(t.TempDir(), "kronos-*.db")
	dbPath.Close()
	makeTestDB(t, dbPath.Name())
	// backupPostgres se llama solo si cfg.DB.Backend=="postgres" con DSN —
	// sin config.json en el dataDir de prueba, cfg vuelve al default
	// (sqlite), así que este test cubre el camino donde no hay Postgres
	// configurado en absoluto. El camino "configurado pero sin pg_dump" se
	// cubre directo en TestBackupPostgres_NoPgDump_ReturnsError.
	if err := runBackupOnce(); err != nil {
		t.Fatalf("runBackupOnce no debería fallar el comando completo: %v", err)
	}
}

func TestBackupPostgres_NoPgDump_ReturnsError(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "out.sql")
	err := backupPostgres("postgresql://user:pass@localhost:5432/db", dst)
	if err == nil {
		t.Skip("pg_dump está disponible en este entorno — no se puede ejercitar el camino de fallback")
	}
}

func TestPruneOldBackups_KeepsOnlyRetentionCount(t *testing.T) {
	dir := t.TempDir()
	total := backupRetention + 5
	for i := 0; i < total; i++ {
		name := filepath.Join(dir, fmt.Sprintf("kronos-202601%02d-000000.db", i+1))
		if err := os.WriteFile(name, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	pruned, err := pruneOldBackups(dir)
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 5 {
		t.Errorf("pruned = %d, want 5", pruned)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != backupRetention {
		t.Errorf("quedaron %d archivos, want %d", len(entries), backupRetention)
	}
}

func TestRestoreBackup_OverwritesBufferAndKeepsPreRestoreCopy(t *testing.T) {
	dataDir := setupTempDataDir(t)
	dbPath := filepath.Join(dataDir, "kronos.db")
	makeTestDB(t, dbPath) // value = "original"

	backupPath := filepath.Join(t.TempDir(), "kronos-backup.db")
	if err := backupSQLite(dbPath, backupPath); err != nil {
		t.Fatal(err)
	}

	// modificar el buffer actual después del backup
	db, err := sql.Open("sqlite3", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE marker SET value = 'modificado'`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if err := restoreBackup(backupPath); err != nil {
		t.Fatalf("restoreBackup: %v", err)
	}

	if got := readMarker(t, dbPath); got != "original" {
		t.Errorf("después de restaurar, value = %q, want %q", got, "original")
	}

	preRestore := dbPath + ".pre-restore"
	if _, err := os.Stat(preRestore); err != nil {
		t.Fatalf("esperaba una copia .pre-restore del estado modificado: %v", err)
	}
	if got := readMarker(t, preRestore); got != "modificado" {
		t.Errorf(".pre-restore tiene %q, want %q (el estado de antes de restaurar)", got, "modificado")
	}
}

func TestRestoreBackup_MissingFile_ReturnsError(t *testing.T) {
	setupTempDataDir(t)
	err := restoreBackup(filepath.Join(t.TempDir(), "no-existe.db"))
	if err == nil {
		t.Fatal("esperaba error con un backup inexistente")
	}
}

func TestRunBackupLoop_StopsOnContextCancel(t *testing.T) {
	dataDir := setupTempDataDir(t)
	dbPath := filepath.Join(dataDir, "kronos.db")
	makeTestDB(t, dbPath)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runBackupLoop(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runBackupLoop no terminó tras cancelar el context")
	}
}
