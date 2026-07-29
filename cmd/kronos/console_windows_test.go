//go:build windows

package main

import (
	"testing"

	"golang.org/x/sys/windows"
)

// TestHasValidStdHandle_TrueWhenPiped confirma la mitad más importante del
// chequeo que evita romper el stdio de kronos mcp/hooks: cuando el proceso
// SÍ tiene un handle de E/S real (el caso de go test, que corre con
// stdout/stderr conectados por el runner), hasValidStdHandle debe devolver
// true — señal de "no tocar nada, dejar el pipe/consola tal cual vino".
func TestHasValidStdHandle_TrueWhenPiped(t *testing.T) {
	if !hasValidStdHandle(windows.STD_OUTPUT_HANDLE) {
		t.Error("hasValidStdHandle(STD_OUTPUT_HANDLE) = false, esperaba true — el proceso de test tiene stdout real")
	}
}
