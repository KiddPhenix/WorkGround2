package config

import (
	"github.com/BurntSushi/toml"

	fileencoding "workground2/internal/fileutil/encoding"
)

func decodeTOMLFile(path string, value any) (toml.MetaData, error) {
	data, err := fileencoding.ReadFileUTF8(path)
	if err != nil {
		return toml.MetaData{}, err
	}
	return toml.Decode(string(data), value)
}
