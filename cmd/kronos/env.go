package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const envUsage = `Uso: kronos env [--json]

Sondea este entorno y lista lo que hay COMPROBADO (navegador headless, CLIs,
puertos), con la hora de la comprobación. Es el destino del puntero que los
hooks imprimen al arrancar: "si dudás de una capacidad, comprobala".

No inventa: si algo no está, lo dice con "no". Y no describe el proyecto —
para eso está la memoria.

  -h, --help   Muestra esta ayuda
  --json       Salida en JSON (para scripts)

Lo último que sondeó queda cacheado en <datadir>/env.json, así que volver a
correrlo es instantáneo y se puede consultar sin re-ejecutar todo.
`

type envFact struct {
	Clave string `json:"clave"`
	Valor string `json:"valor"`
	Hay   bool   `json:"hay"`
}

func runEnv(args []string) error {
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Print(envUsage)
			return nil
		case "--json":
			// se maneja abajo
		}
	}
	jsonOut := false
	for _, a := range args {
		if a == "--json" {
			jsonOut = true
		}
	}

	facts := probeEnv()
	cuando := time.Now().Format("2006-01-02 15:04")

	if jsonOut {
		blob, _ := json.MarshalIndent(map[string]any{
			"comprobado": cuando,
			"facts":      facts,
		}, "", "  ")
		fmt.Println(string(blob))
	} else {
		fmt.Printf("[kronos:env] comprobado %s\n", cuando)
		for _, f := range facts {
			marca := "no"
			if f.Hay {
				marca = "si"
			}
			fmt.Printf("  %-22s %s  %s\n", f.Clave, marca, f.Valor)
		}
		fmt.Println("  (los que dicen 'no' no están instalados: no los afirmes ni los uses)")
	}

	// Caché: reejecutar el comando es instantáneo y deja rastro de la última
	// comprobación para que el agente sepa si el dato está fresco.
	if dir := envCacheDir(); dir != "" {
		blob, _ := json.MarshalIndent(map[string]any{
			"comprobado": cuando,
			"facts":      facts,
		}, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "env.json"), blob, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "no pude escribir la cache de env: %v\n", err)
		}
	}
	return nil
}

// envCacheDir: junto al resto de los datos de kronos. Vacío si no hay HOME.
func envCacheDir() string {
	home := os.Getenv("HOME")
	if home == "" {
		return ""
	}
	dir := filepath.Join(home, ".local", "share", "kronos")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	return dir
}

// probeEnv comprueba, no supone: cada hecho sale de ejecutar algo.
func probeEnv() []envFact {
	var facts []envFact
	add := func(clave string, hay bool, valor string) {
		facts = append(facts, envFact{Clave: clave, Valor: valor, Hay: hay})
	}

	// Navegador headless: el CLI de Playwright y, por separado, los binarios
	// en caché. Los dos importan: el CLI sin navegadores no sirve para nada.
	if ruta, err := exec.LookPath("playwright"); err == nil {
		ver := primeraLinea(exec.Command(ruta, "--version"))
		navegadores := ""
		if home := os.Getenv("HOME"); home != "" {
			entradas, _ := filepath.Glob(filepath.Join(home, ".cache", "ms-playwright", "chromium*"))
			if len(entradas) > 0 {
				nombres := make([]string, 0, len(entradas))
				for _, e := range entradas {
					nombres = append(nombres, filepath.Base(e))
				}
				navegadores = strings.Join(nombres, ", ")
			}
		}
		valor := fmt.Sprintf("%s en %s", ver, ruta)
		if navegadores != "" {
			valor += " | navegadores en cache: " + navegadores
		} else {
			valor += " | SIN navegadores en cache (playwright install)"
		}
		add("navegador headless", true, valor)
	} else {
		add("navegador headless", false, "sin playwright en PATH")
	}

	// CLIs que usa el trabajo real.
	for _, c := range []struct{ clave, bin string }{
		{"claude (LLM por suscripcion)", "claude"},
		{"psql (cliente Postgres)", "psql"},
		{"infisical (secretos)", "infisical"},
		{"tailscale (red privada)", "tailscale"},
		{"git", "git"},
		{"go", "go"},
		{"golangci-lint", "golangci-lint"},
		{"uv (python venv)", "uv"},
		{"docker", "docker"},
	} {
		if ruta, err := exec.LookPath(c.bin); err == nil {
			add(c.clave, true, ruta)
		} else {
			add(c.clave, false, "no esta en PATH")
		}
	}

	// Puertos que importan: la base de kronos y el daemon.
	for _, p := range []struct {
		clave string
		dir   string
	}{
		{"daemon kronos :4317", os.Getenv("HOME") + "/.local/bin/kronos"},
	} {
		if _, err := os.Stat(p.dir); err == nil {
			add(p.clave, true, "binario presente")
		} else {
			add(p.clave, false, "sin binario")
		}
	}

	// Nodos de tailscale: es lo que hace falta para llegar a la laptop.
	if ruta, err := exec.LookPath("tailscale"); err == nil {
		out, err := exec.Command(ruta, "status", "--peers=false").Output()
		if err == nil {
			lineas := strings.Split(strings.TrimSpace(string(out)), "\n")
			add("nodo propio (tailnet)", true, strings.Join(campos(lineas, 0, 3), " "))
		}
	}

	return facts
}

func primeraLinea(cmd *exec.Cmd) string {
	out, err := cmd.Output()
	if err != nil {
		return "instalado"
	}
	lineas := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lineas) == 0 {
		return "instalado"
	}
	return strings.TrimSpace(lineas[0])
}

func campos(lineas []string, desde, cuantos int) []string {
	var out []string
	for i := desde; i < len(lineas) && i < desde+cuantos; i++ {
		if f := strings.Fields(lineas[i]); len(f) > 0 {
			out = append(out, f[0])
		}
	}
	return out
}
