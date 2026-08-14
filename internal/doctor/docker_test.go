package doctor

import (
	"context"
	"strings"
	"testing"
)

// TestCheckDockerAvailable_DockerNotInstalled_ClearError reproduce el caso
// real que motivó separar este chequeo: antes, "docker no instalado" y
// "Docker Desktop no está corriendo" terminaban en el mismo error crudo de
// `docker run`, sin decir cuál de las dos causas era. Acá se fuerza la
// primera vaciando PATH — sin el binario docker en absoluto, el mensaje
// debe decir explícitamente que hay que instalarlo, no un error de conexión
// genérico.
func TestCheckDockerAvailable_DockerNotInstalled_ClearError(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // directorio vacío — garantiza que no hay "docker" en PATH

	err := checkDockerAvailable(context.Background())
	if err == nil {
		t.Fatal("esperaba error sin docker en PATH")
	}
	if !strings.Contains(err.Error(), "no está instalado") {
		t.Errorf("error = %q, quería que mencione que docker no está instalado", err.Error())
	}
}

// TestDockerContainerRunning_NonexistentContainer_FalseFalse confirma el
// contrato para un contenedor que nunca existió — determinista sin importar
// si docker está instalado en el entorno del test: si docker no está,
// exec falla al arrancar (mismo resultado); si está pero el contenedor no
// existe, `docker inspect` falla con "No such object" (mismo resultado).
func TestDockerContainerRunning_NonexistentContainer_FalseFalse(t *testing.T) {
	exists, running := dockerContainerRunning(context.Background(), "kronos-test-contenedor-que-no-existe-jamas")
	if exists {
		t.Error("un contenedor que nunca existió no debería reportar exists=true")
	}
	if running {
		t.Error("un contenedor que nunca existió no debería reportar running=true")
	}
}
