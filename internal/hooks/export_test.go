package hooks

// export_test.go expone símbolos internos únicamente para tests en el
// paquete hooks_test — no cambia comportamiento de producción.

// DigestFactIsGrounded expone digestFactIsGrounded para el test
// TestDigestFactIsGrounded_BloqueaFabricacionReal (paquete hooks_test),
// que reproduce el bug real de conflación (obs 231072704912801792).
var DigestFactIsGrounded = digestFactIsGrounded
