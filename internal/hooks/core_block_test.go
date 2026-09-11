package hooks_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/hooks"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

// setTempConfigDir redirige config.ConfigPath a un directorio temporal —
// mismo patrón que internal/config/config_test.go, duplicado acá porque
// vive en otro paquete de test.
func setTempConfigDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", dir)
	} else {
		t.Setenv("XDG_CONFIG_HOME", dir)
		t.Setenv("HOME", dir)
	}
	_ = os.MkdirAll(filepath.Join(dir, "kronos"), 0755)
}

func saveObs(t *testing.T, st store.Storer, typ store.ObservationType, scope store.Scope, project, title, content string) {
	t.Helper()
	_, err := st.SaveObservation(context.Background(), store.SaveParams{
		Type:    typ,
		Title:   title,
		Content: content,
		Project: project,
		Scope:   scope,
	})
	if err != nil {
		t.Fatalf("SaveObservation: %v", err)
	}
}

// (a) el presupuesto se respeta: con CharsLimit chico, el bloque resultante
// no lo supera y avisa que fue recortado.
func TestBuildCoreBlock_RespectsCharsBudget(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		title := fmt.Sprintf("Hallazgo largo número %d", i)
		saveObs(t, st, store.TypeDiscovery, store.ScopeProject, "proyecto-x",
			title, strings.Repeat(fmt.Sprintf("contenido de relleno %d ", i), 20))
	}

	limit := 400
	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{CharsLimit: limit, MaxItems: 20})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	if len(block) > limit {
		t.Errorf("bloque de %d chars supera el límite de %d", len(block), limit)
	}
	if !strings.Contains(block, "recortado por presupuesto") {
		t.Errorf("esperaba aviso de recorte, bloque:\n%s", block)
	}
}

// (b) la prioridad se respeta: con presupuesto ajustado entran primero
// globales/preferences y se cae lo menos prioritario (relleno reciente).
func TestBuildCoreBlock_PrioritizesGlobalAndPreference(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	saveObs(t, st, store.TypePattern, store.ScopeGlobal, "proyecto-x", "Patron global reutilizable", "este patron aplica a cualquier proyecto")
	saveObs(t, st, store.TypePreference, store.ScopeProject, "proyecto-x", "Preferencia de Jerry", "nunca usar voseo ni jerga rioplatense")

	// Muchísimo relleno reciente (cada uno con contenido distinto, para que
	// el dedup por contenido normalizado no los colapse en uno solo) para
	// forzar que el presupuesto ajustado solo alcance para los dos items
	// prioritarios de arriba.
	for i := 0; i < 30; i++ {
		saveObs(t, st, store.TypeDiscovery, store.ScopeProject, "proyecto-x",
			fmt.Sprintf("Relleno reciente %d", i), strings.Repeat(fmt.Sprintf("y%d", i), 80))
	}

	block, err := hooks.BuildCoreBlock(ctx, st, "proyecto-x", hooks.CoreBlockOptions{CharsLimit: 350, MaxItems: 20})
	if err != nil {
		t.Fatalf("BuildCoreBlock: %v", err)
	}
	if !strings.Contains(block, "Patron global reutilizable") {
		t.Errorf("esperaba que la observación global entrara primero, bloque:\n%s", block)
	}
	if !strings.Contains(block, "Preferencia de Jerry") {
		t.Errorf("esperaba que la preferencia entrara, bloque:\n%s", block)
	}
	if strings.Contains(block, "Relleno reciente") {
		t.Errorf("esperaba que el relleno de menor prioridad quedara afuera, bloque:\n%s", block)
	}
	if !strings.Contains(block, "recortado por presupuesto") {
		t.Errorf("esperaba aviso de recorte, bloque:\n%s", block)
	}
}

// (c) enabled: false en config ⇒ el hook no imprime nada de core.
func TestRunSessionStart_CoreDisabled_NoCoreBlock(t *testing.T) {
	setupTempDataDir(t)
	setTempConfigDir(t)
	st := newTestStore(t)
	ctx := context.Background()

	cfg := config.Default()
	cfg.Core.Enabled = false
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save config: %v", err)
	}

	saveObs(t, st, store.TypePattern, store.ScopeGlobal, "proyecto-y", "Patron global", "contenido")

	in := hooks.Input{SessionID: "test-core-disabled", CWD: "/home/x/proyecto-y"}
	out := captureStdout(t, func() {
		if err := hooks.RunSessionStart(ctx, in, st); err != nil {
			t.Fatalf("RunSessionStart: %v", err)
		}
	})

	if strings.Contains(out, "[kronos:core]") {
		t.Errorf("con core.enabled=false no debería aparecer el bloque core, output:\n%s", out)
	}
}

// enabled: true (default) sí debe imprimir el bloque cuando hay contenido.
func TestRunSessionStart_CoreEnabled_PrintsCoreBlock(t *testing.T) {
	setupTempDataDir(t)
	setTempConfigDir(t)
	st := newTestStore(t)
	ctx := context.Background()

	cfg := config.Default()
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save config: %v", err)
	}

	saveObs(t, st, store.TypePattern, store.ScopeGlobal, "proyecto-z", "Patron global visible", "contenido")

	in := hooks.Input{SessionID: "test-core-enabled", CWD: "/home/x/proyecto-z"}
	out := captureStdout(t, func() {
		if err := hooks.RunSessionStart(ctx, in, st); err != nil {
			t.Fatalf("RunSessionStart: %v", err)
		}
	})

	if !strings.Contains(out, "[kronos:core]") {
		t.Errorf("esperaba bloque core en el output:\n%s", out)
	}
	if !strings.Contains(out, "Patron global visible") {
		t.Errorf("esperaba la observación global en el bloque, output:\n%s", out)
	}
}
