package issues

import "embed"

//go:embed create.json assign.json merge.json
var schemaFS embed.FS

func Schema(name string) []byte {
	data, err := schemaFS.ReadFile(name + ".json")
	if err != nil {
		panic("issues: unknown schema " + name + ": " + err.Error())
	}
	return data
}
