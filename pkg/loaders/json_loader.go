package loaders

import (
	"fmt"

	"github.com/hc12r/brokolisql-go/pkg/common"
)

type JSONLoader struct{}

func (l *JSONLoader) Load(filePath string) (*common.DataSet, error) {
	fileContent, err := common.SafeReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read JSON file: %w", err)
	}

	data, err := common.ParseJSONData(fileContent)
	if err != nil {
		return nil, fmt.Errorf("failed to parse JSON file: %w", err)
	}

	return common.ConvertToDataSet(data), nil
}
