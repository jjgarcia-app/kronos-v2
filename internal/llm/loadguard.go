package llm

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// loadOverThreshold decide si la carga actual de la máquina supera
// maxLoadPerCPU — motivado por un bug real medido en producción: con la
// máquina saturada, cada intento fallido de generación (Ollama colgado bajo
// carga) dejaba un proceso local girando a >200% CPU durante minutos, lo que
// empeoraba la carga y hacía fallar el intento siguiente — un círculo
// vicioso. Chequear /proc/loadavg antes de intentar corta el círculo en vez
// de dejar que el timeout de cada llamada lo descubra tarde.
//
// maxLoadPerCPU <= 0 desactiva el guardián (siempre false). Si /proc/loadavg
// no se puede leer (otro SO, por ejemplo), no bloquea: over=false.
func loadOverThreshold(maxLoadPerCPU float64) (over bool, load1 float64, cpus int) {
	over, load1, cpus, _ = LoadGuardStatus(maxLoadPerCPU)
	return over, load1, cpus
}

// LoadGuardStatus expone loadOverThreshold para `kronos doctor` (ver
// internal/doctor) — available es false cuando el guardián está desactivado
// (maxLoadPerCPU <= 0) o /proc/loadavg no se pudo leer, para que el caller
// distinga "no está salteando nada" de "no sé si está salteando algo".
func LoadGuardStatus(maxLoadPerCPU float64) (skipping bool, load1 float64, cpus int, available bool) {
	if maxLoadPerCPU <= 0 {
		return false, 0, 0, false
	}
	l1, err := readLoadAvg1()
	if err != nil {
		return false, 0, 0, false
	}
	c := runtime.NumCPU()
	if c <= 0 {
		c = 1
	}
	return l1/float64(c) > maxLoadPerCPU, l1, c, true
}

// readLoadAvg1 lee el promedio de carga de 1 minuto desde /proc/loadavg
// (Linux). En otros SO simplemente no existe el archivo — el caller trata
// ese error como "no bloquear", no como una falla real.
func readLoadAvg1() (float64, error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, fmt.Errorf("formato inesperado de /proc/loadavg: %q", string(data))
	}
	return strconv.ParseFloat(fields[0], 64)
}
