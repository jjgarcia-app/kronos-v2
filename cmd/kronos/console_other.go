//go:build !windows

package main

// attachConsoleIfInteractive es un no-op fuera de Windows — el problema que
// resuelve (parpadeo de consola al lanzar un binario nativo desde Git Bash)
// es específico de Windows.
func attachConsoleIfInteractive() {}
