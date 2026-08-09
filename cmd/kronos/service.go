package main

import (
	"fmt"

	"github.com/kardianos/service"
)

// serviceConfig describe el daemon de kronos ante el SO — mismo nombre en
// las tres plataformas que soporta kardianos/service (Windows Service,
// systemd en Linux, launchd en macOS).
var serviceConfig = &service.Config{
	Name:        "kronos",
	DisplayName: "Kronos Memory Daemon",
	Description: "Daemon compartido de memoria persistente de Kronos — un solo proceso para todas las sesiones de Claude Code en esta máquina.",
}

// kronosProgram implementa service.Interface. Start/Stop deben devolver
// rápido — el trabajo real corre en la goroutine de run(), igual que
// runServe ya hace con hs.Start() (no bloqueante) + <-ctx.Done().
type kronosProgram struct {
	stop chan struct{}
}

func (p *kronosProgram) Start(s service.Service) error {
	p.stop = make(chan struct{})
	go p.run()
	return nil
}

func (p *kronosProgram) run() {
	// runServeWithStop con --daemon-mode ya hace exactamente lo que un
	// servicio de SO necesita: bind del puerto, log a archivo (no a una
	// consola que un servicio no tiene), loop hasta que el context se
	// cancele. p.stop es lo que conecta un Stop() real del SCM/systemd con
	// ese loop — sin esto, Stop() no tendría forma de frenar el daemon.
	_ = runServeWithStop(p.stop, "--daemon-mode")
}

func (p *kronosProgram) Stop(s service.Service) error {
	if p.stop != nil {
		close(p.stop)
	}
	return nil
}

// runServiceCmd implementa `kronos service <install|uninstall|start|stop|restart|status|run>`.
//
// "run" es el subcomando que el propio SO invoca internamente al arrancar
// el servicio (kardianos lo necesita para saber que este proceso corre
// "como servicio", no interactivo) — no es para uso manual directo.
//
// install/uninstall modifican el Service Control Manager de Windows (o
// systemd/launchd) — requieren privilegios de administrador y dejan un
// registro persistente en el SO. A propósito NO se ejecutan solos: hace
// falta que el usuario corra el comando explícitamente.
func runServiceCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("uso: kronos service <install|uninstall|start|stop|restart|status|run>")
	}

	prg := &kronosProgram{}
	s, err := service.New(prg, serviceConfig)
	if err != nil {
		return fmt.Errorf("crear definición de servicio: %w", err)
	}

	switch args[0] {
	case "install":
		if err := s.Install(); err != nil {
			return fmt.Errorf("instalar servicio (¿corriste esto como administrador?): %w", err)
		}
		fmt.Println("Servicio 'kronos' instalado — arrancá con `kronos service start`")
		return nil
	case "uninstall":
		if err := s.Uninstall(); err != nil {
			return fmt.Errorf("desinstalar servicio: %w", err)
		}
		fmt.Println("Servicio 'kronos' desinstalado")
		return nil
	case "start":
		if err := s.Start(); err != nil {
			return fmt.Errorf("arrancar servicio: %w", err)
		}
		fmt.Println("Servicio 'kronos' arrancado")
		return nil
	case "stop":
		if err := s.Stop(); err != nil {
			return fmt.Errorf("detener servicio: %w", err)
		}
		fmt.Println("Servicio 'kronos' detenido")
		return nil
	case "restart":
		if err := s.Restart(); err != nil {
			return fmt.Errorf("reiniciar servicio: %w", err)
		}
		fmt.Println("Servicio 'kronos' reiniciado")
		return nil
	case "status":
		st, err := s.Status()
		if err != nil {
			return fmt.Errorf("consultar estado: %w", err)
		}
		fmt.Println(serviceStatusString(st))
		return nil
	case "run":
		// invocado por el SO, no por una persona — bloquea hasta que el SO
		// pida parar.
		return s.Run()
	default:
		return fmt.Errorf("subcomando desconocido %q — uso: kronos service <install|uninstall|start|stop|restart|status|run>", args[0])
	}
}

func serviceStatusString(st service.Status) string {
	switch st {
	case service.StatusRunning:
		return "corriendo"
	case service.StatusStopped:
		return "detenido"
	default:
		return "desconocido"
	}
}
