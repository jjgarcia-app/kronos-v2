package embeddings

import (
	"context"
	"sync"
	"time"
)

// recallCacheTTL es la ventana en la que un mismo texto (típicamente el
// prompt del usuario, repetido o casi-repetido dentro de una sesión de
// trabajo) no vuelve a pagar el round-trip contra Ollama. Medido en esta
// máquina: 800ms-6s por llamada de embedding, según carga (Ollama compartido
// con el daemon y otras sesiones). 10 minutos alcanza para cubrir
// reformulaciones/reintentos del mismo prompt dentro del mismo tramo de
// conversación, sin arriesgar servir un vector stale para una sesión larga.
const recallCacheTTL = 10 * time.Minute

// embedCache es un cache en memoria (por proceso) de embeddings ya
// calculados, con expiración por entrada. No hay límite de tamaño: vive
// mientras dure el proceso (el daemon) o la corrida corta del hook — en
// ambos casos el volumen de texto embebido por ventana de 10 minutos es
// chico (prompts + observaciones nuevas), no vale la pena una LRU.
type embedCache struct {
	mu    sync.Mutex
	items map[string]cachedEmbedding
}

type cachedEmbedding struct {
	vec     []float32
	expires time.Time
}

func newEmbedCache() *embedCache {
	return &embedCache{items: make(map[string]cachedEmbedding)}
}

// wrap envuelve fn para que, dentro de recallCacheTTL, el mismo texto exacto
// no dispare una nueva llamada al provider (Ollama).
func (c *embedCache) wrap(fn EmbeddingFunc) EmbeddingFunc {
	return func(ctx context.Context, text string) ([]float32, error) {
		now := time.Now()

		c.mu.Lock()
		if e, ok := c.items[text]; ok && now.Before(e.expires) {
			c.mu.Unlock()
			return e.vec, nil
		}
		c.mu.Unlock()

		vec, err := fn(ctx, text)
		if err != nil {
			return nil, err
		}

		c.mu.Lock()
		c.items[text] = cachedEmbedding{vec: vec, expires: now.Add(recallCacheTTL)}
		c.mu.Unlock()

		return vec, nil
	}
}

// defaultEmbedCache es el cache de embeddings compartido por proceso —
// AutoFunc lo usa para que cualquier caller (runRecall, backfill, etc.) se
// beneficie de texto repetido sin coordinar nada explícitamente.
var defaultEmbedCache = newEmbedCache()

// vectorStoreCacheTTL: mismo razonamiento que recallCacheTTL, pero para la
// instancia de *VectorStore por dataDir — reabrir chromem.NewPersistentDB
// implica releer el índice completo de disco, costo que no vale la pena
// pagar de nuevo si el proceso ya la abrió hace poco.
const vectorStoreCacheTTL = 10 * time.Minute

var (
	vsCacheMu    sync.Mutex
	vsCacheEntry *vsCacheItem
)

type vsCacheItem struct {
	dataDir string
	vs      *VectorStore
	expires time.Time
}

// cachedVectorStore devuelve el *VectorStore cacheado para dataDir si sigue
// vigente (dentro de vectorStoreCacheTTL), o (nil, false) si hay que abrir
// uno nuevo.
func cachedVectorStore(dataDir string) (*VectorStore, bool) {
	vsCacheMu.Lock()
	defer vsCacheMu.Unlock()
	if vsCacheEntry == nil || vsCacheEntry.dataDir != dataDir {
		return nil, false
	}
	if time.Now().After(vsCacheEntry.expires) {
		return nil, false
	}
	return vsCacheEntry.vs, true
}

func storeCachedVectorStore(dataDir string, vs *VectorStore) {
	vsCacheMu.Lock()
	defer vsCacheMu.Unlock()
	vsCacheEntry = &vsCacheItem{dataDir: dataDir, vs: vs, expires: time.Now().Add(vectorStoreCacheTTL)}
}
