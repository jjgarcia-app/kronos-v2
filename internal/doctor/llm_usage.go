package doctor

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/llm"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
)

// usageResultOrder fija el orden de presentación de los resultados de
// generación en `kronos doctor` — sin esto, iterar un map da orden aleatorio
// entre corridas.
var usageResultOrder = []string{
	llm.UsageResultOK, llm.UsageResultError,
	llm.UsageResultSkippedLoad, llm.UsageResultSkippedBreaker,
}

var usageResultLabel = map[string]string{
	llm.UsageResultOK:             "ok",
	llm.UsageResultError:          "error",
	llm.UsageResultSkippedLoad:    "saltada por carga",
	llm.UsageResultSkippedBreaker: "saltada por cortacircuitos",
}

// checkLLMUsage reporta cuántas llamadas de generación (digest, captura
// pasiva, judge — ver internal/llm.Client) se hicieron en la hora y el día
// en curso, desglosadas por proveedor/resultado, y hace cuánto fue la
// última — hoy no hay forma de ver cuánto consume kronos de la suscripción
// de Claude Code (`claude -p`) sin leer el archivo de uso a mano.
func checkLLMUsage() Check {
	dataDir, err := platform.DataDir()
	if err != nil {
		return Check{Name: "Uso de generación LLM", Detail: "no se pudo resolver el data dir", Status: StatusWarn}
	}

	state := llm.NewUsage(llm.DefaultUsagePath(dataDir)).State()
	if len(state.Buckets) == 0 || state.LastAt.IsZero() {
		return Check{
			Name:   "Uso de generación LLM",
			Detail: "sin datos — todavía no se registró ninguna llamada de generación",
			Status: StatusOK,
		}
	}

	return Check{Name: "Uso de generación LLM", Detail: formatUsageDetail(state, time.Now()), Status: StatusOK}
}

// formatUsageDetail arma la línea "generación: N llamada(s) en la última
// hora (...) | hoy: M (...) | última: hace T". "Última hora" y "hoy" se
// aproximan con la granularidad de almacenamiento real (buckets por hora,
// ver internal/llm.UsageBucket): la primera es el bucket de la hora en
// curso, no una ventana deslizante de 60 minutos exactos — con el volumen
// real (unas pocas llamadas por hora) la diferencia no importa, y guardar
// granularidad de minuto infla el archivo sin necesidad.
func formatUsageDetail(state llm.UsageState, now time.Time) string {
	currentHour := now.Truncate(time.Hour)
	today := now.Format("2006-01-02")

	var hourBuckets, todayBuckets []llm.UsageBucket
	for _, b := range state.Buckets {
		if b.HourStart.Equal(currentHour) {
			hourBuckets = append(hourBuckets, b)
		}
		if b.HourStart.Local().Format("2006-01-02") == today {
			todayBuckets = append(todayBuckets, b)
		}
	}

	detail := fmt.Sprintf("generación: %s en la última hora%s",
		pluralLlamadas(sumBuckets(hourBuckets)), providerBreakdown(hourBuckets))
	detail += fmt.Sprintf(" | hoy: %d%s", sumBuckets(todayBuckets), resultBreakdownSuffix(todayBuckets))
	detail += " | última: hace " + formatDuration(now.Sub(state.LastAt))

	return detail
}

func sumBuckets(buckets []llm.UsageBucket) int {
	total := 0
	for _, b := range buckets {
		total += b.Count
	}
	return total
}

func pluralLlamadas(n int) string {
	if n == 1 {
		return "1 llamada"
	}
	return fmt.Sprintf("%d llamadas", n)
}

// providerBreakdown arma "(claude-cli 4: 4 ok)" agrupando por proveedor y,
// dentro de cada uno, por resultado — "" si no hay buckets.
func providerBreakdown(buckets []llm.UsageBucket) string {
	if len(buckets) == 0 {
		return ""
	}
	byProvider := map[string]map[string]int{}
	var providers []string
	for _, b := range buckets {
		if _, ok := byProvider[b.Provider]; !ok {
			byProvider[b.Provider] = map[string]int{}
			providers = append(providers, b.Provider)
		}
		byProvider[b.Provider][b.Result] += b.Count
	}
	sort.Strings(providers)

	parts := make([]string, 0, len(providers))
	for _, p := range providers {
		parts = append(parts, fmt.Sprintf("%s %d: %s", p, sumMap(byProvider[p]), resultBreakdown(byProvider[p])))
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// resultBreakdownSuffix arma "(11 ok, 1 error)" agregando por resultado sin
// distinguir proveedor — usado en el resumen "hoy", que no necesita el
// desglose por proveedor que sí tiene la última hora.
func resultBreakdownSuffix(buckets []llm.UsageBucket) string {
	if len(buckets) == 0 {
		return ""
	}
	byResult := map[string]int{}
	for _, b := range buckets {
		byResult[b.Result] += b.Count
	}
	breakdown := resultBreakdown(byResult)
	if breakdown == "" {
		return ""
	}
	return " (" + breakdown + ")"
}

func resultBreakdown(byResult map[string]int) string {
	var parts []string
	for _, r := range usageResultOrder {
		if c := byResult[r]; c > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c, usageResultLabel[r]))
		}
	}
	return strings.Join(parts, ", ")
}

func sumMap(m map[string]int) int {
	total := 0
	for _, c := range m {
		total += c
	}
	return total
}
