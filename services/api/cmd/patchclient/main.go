// patchclient rewrites authclient/oas_json_gen.go after ogen generation so
// that decoders skip unknown JSON fields (e.g. Huma's $schema injection)
// instead of returning an error. Run via go:generate — see generate.go.
package main

import (
	"log"
	"os"
	"strings"
)

const (
	target = "authclient/oas_json_gen.go"

	from = "\t\tdefault:\n\t\t\treturn errors.Errorf(\"unexpected field %q\", k)\n\t\t}"
	to   = "\t\tdefault:\n\t\t\tif err := d.Skip(); err != nil {\n\t\t\t\treturn errors.Wrap(err, \"skip unknown field\")\n\t\t\t}\n\t\t}"
)

func main() {
	data, err := os.ReadFile(target)
	if err != nil {
		log.Fatalf("read %s: %v", target, err)
	}
	patched := strings.ReplaceAll(string(data), from, to)
	if err := os.WriteFile(target, []byte(patched), 0o644); err != nil {
		log.Fatalf("write %s: %v", target, err)
	}
	log.Printf("patched %s", target)
}
