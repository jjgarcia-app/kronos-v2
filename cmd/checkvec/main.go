package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jjgarcia-app/kronos-v2/internal/config"
	"github.com/jjgarcia-app/kronos-v2/internal/embeddings"
	"github.com/jjgarcia-app/kronos-v2/internal/platform"
	"github.com/jjgarcia-app/kronos-v2/internal/store"
)

func main() {
	ctx := context.Background()
	dataDir, _ := platform.DataDir()
	vs, err := embeddings.New(ctx, dataDir)
	if err != nil || vs == nil {
		fmt.Println("sin vector store:", err)
		os.Exit(1)
	}
	cfg, _ := config.Load()
	st, err := store.NewPostgres(cfg.DB.PostgresDSN)
	if err != nil {
		fmt.Println("err postgres:", err)
		os.Exit(1)
	}
	obs, err := st.ListAll(ctx, "")
	if err != nil {
		fmt.Println("err listall:", err)
		os.Exit(1)
	}
	missing := 0
	byProject := map[string]int{}
	for _, o := range obs {
		if !vs.Has(ctx, o.ID) {
			missing++
			byProject[o.Project]++
		}
	}
	fmt.Printf("total observaciones: %d | sin indexar: %d\n", len(obs), missing)
	for p, n := range byProject {
		if n > 0 {
			fmt.Printf("  %s: %d\n", p, n)
		}
	}
	_ = time.Now
}
