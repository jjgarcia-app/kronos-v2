package main

// hasHelpFlag detecta --help/-h en cualquier posición de args. Los
// subcomandos (export, gc, doctor) parsean sus propios flags a mano con un
// switch simple; un flag desconocido ahí cae al default y se ignora en
// silencio, así que --help terminaba ejecutando el comando completo en vez
// de mostrar ayuda. Se revisa esto antes de tocar nada.
func hasHelpFlag(args []string) bool {
	for _, a := range args {
		if a == "--help" || a == "-h" {
			return true
		}
	}
	return false
}
