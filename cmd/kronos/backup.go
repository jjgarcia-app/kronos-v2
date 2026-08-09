package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// backupInterval: cada cuánto el daemon corre un backup automático solo. 24h
// alcanza para el caso de uso real — un desarrollador trabajando en su
// propia máquina, no un servicio con SLA de RPO ajustado.
const backupInterval = 24 * time.Hour

// runBackupLoop corre en background mientras el daemon vive — reemplaza el
// hábito manual de copiar kronos.db antes de un cambio grande por algo
// automático, sin depender de que el usuario configure Task Scheduler/cron
// aparte. Un backup inicial al arrancar, después cada backupInterval. Nunca
// tira el daemon abajo por un fallo de backup — solo lo loguea (va a
// daemon.log en modo daemon, ver redirectLogsToFile).
func runBackupLoop(ctx context.Context) {
	runOnce := func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "backup automático: panic recuperado: %v\n", r)
			}
		}()
		if err := runBackupOnce(); err != nil {
			fmt.Fprintf(os.Stderr, "backup automático falló: %v\n", err)
		}
	}

	runOnce()
	ticker := time.NewTicker(backupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}

// backupRetention: cuántos backups locales de SQLite se conservan — más
// viejos que esto se podan en cada corrida. 14 cubre dos semanas de trabajo
// diario sin acumular sin límite.
const backupRetention = 14

// runBackup implementa `kronos backup [--list] [--restore <archivo>]`.
//
// Reemplaza el hábito manual de copiar kronos.db a mano antes de un cambio
// grande (ej. kronos.db.backup-pre-v3-fixes, visto en producción) por algo
// que corre solo. El daemon llama a esto una vez por día (ver serve.go) —
// "automático" en el sentido de que no depende de que nadie se acuerde de
// correrlo, sin necesitar Task Scheduler/cron configurado aparte.
func runBackup(args []string) error {
	if len(args) > 0 && args[0] == "--list" {
		return listBackups()
	}
	if len(args) > 0 && args[0] == "--restore" {
		if len(args) < 2 {
			return fmt.Errorf("uso: kronos backup --restore <archivo>")
		}
		return restoreBackup(args[1])
	}
	return runBackupOnce()
}

func backupDir() (string, error) {
	dataDir, err := platform.DataDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

// runBackupOnce hace un backup del buffer SQLite local y, si hay Postgres
// configurado y pg_dump está disponible, también un dump de Postgres.
// Nunca falla el comando completo por el lado de Postgres — un backup local
// exitoso ya es mejor que nada, y pg_dump ausente no debería bloquear el
// backup del buffer.
func runBackupOnce() error {
	dir, err := backupDir()
	if err != nil {
		return fmt.Errorf("backup dir: %w", err)
	}
	ts := time.Now().UTC().Format("20060102-150405")

	dbPath, err := platform.DBPath()
	if err != nil {
		return fmt.Errorf("db path: %w", err)
	}
	sqliteBackup := filepath.Join(dir, fmt.Sprintf("kronos-%s.db", ts))
	if err := backupSQLite(dbPath, sqliteBackup); err != nil {
		return fmt.Errorf("backup sqlite: %w", err)
	}
	fmt.Printf("Backup local: %s\n", sqliteBackup)

	cfg, _ := config.Load()
	if cfg.DB.Backend == "postgres" && cfg.DB.PostgresDSN != "" {
		pgBackup := filepath.Join(dir, fmt.Sprintf("postgres-%s.sql", ts))
		if err := backupPostgres(cfg.DB.PostgresDSN, pgBackup); err != nil {
			fmt.Fprintf(os.Stderr, "warn: backup de Postgres omitido: %v\n", err)
		} else {
			fmt.Printf("Backup Postgres: %s\n", pgBackup)
		}
	}

	pruned, err := pruneOldBackups(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: no se pudo podar backups viejos: %v\n", err)
	} else if pruned > 0 {
		fmt.Printf("Podados %d backups locales más allá de la retención (%d)\n", pruned, backupRetention)
	}
	return nil
}

// backupSQLite hace un checkpoint del WAL antes de copiar — sin esto,
// escrituras recientes que todavía viven en el archivo -wal quedarían
// afuera de la copia, dando un backup incompleto silenciosamente.
func backupSQLite(src, dst string) error {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)", src)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("wal checkpoint: %w", err)
	}
	db.Close()

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func backupPostgres(dsn, dst string) error {
	if _, err := exec.LookPath("pg_dump"); err != nil {
		return fmt.Errorf("pg_dump no está en PATH — instalá el cliente de Postgres para backups automáticos de la DB remota")
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.Command("pg_dump", dsn)
	cmd.Stdout = f
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_dump: %w: %s", err, stderr.String())
	}
	return nil
}

func pruneOldBackups(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var sqliteBackups []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "kronos-") && strings.HasSuffix(e.Name(), ".db") {
			sqliteBackups = append(sqliteBackups, e.Name())
		}
	}
	sort.Strings(sqliteBackups) // el timestamp en el nombre ordena cronológicamente
	if len(sqliteBackups) <= backupRetention {
		return 0, nil
	}
	toRemove := sqliteBackups[:len(sqliteBackups)-backupRetention]
	for _, name := range toRemove {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return 0, err
		}
	}
	return len(toRemove), nil
}

func listBackups() error {
	dir, err := backupDir()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Println("no hay backups todavía — corré `kronos backup` para crear el primero")
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Println(n)
	}
	return nil
}

// restoreBackup sobreescribe el buffer SQLite local con un backup dado.
// Destructivo a propósito — quien lo corre ya decidió que quiere volver a
// ese punto. No toca Postgres: si el primary está sano, la próxima
// escritura simplemente sigue yendo ahí; restaurar el buffer local no
// resucita datos que Postgres ya no tiene.
func restoreBackup(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("backup no encontrado: %s", path)
	}
	dbPath, err := platform.DBPath()
	if err != nil {
		return fmt.Errorf("db path: %w", err)
	}

	preRestoreCopy := dbPath + ".pre-restore"
	if _, err := os.Stat(dbPath); err == nil {
		if err := backupSQLite(dbPath, preRestoreCopy); err != nil {
			return fmt.Errorf("no se pudo respaldar el buffer actual antes de restaurar: %w", err)
		}
		fmt.Printf("Buffer actual respaldado en: %s (por si esto era un error)\n", preRestoreCopy)
	}

	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dbPath)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	// borrar -wal/-shm viejos del buffer anterior — si no, SQLite podría
	// intentar reproducir un WAL que ya no corresponde al .db restaurado.
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")

	fmt.Printf("Restaurado desde %s a %s\n", path, dbPath)
	return nil
}
