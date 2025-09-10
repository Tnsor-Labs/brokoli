package loaders

import (
	"brokolisql-go/pkg/common"
	"encoding/xml"
	"fmt"
	"strings"
)

type XMLLoader struct{}

type XMLNode struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:",any,attr"`
	Content  string     `xml:",chardata"`
	Children []XMLNode  `xml:",any"`
}

func (l *XMLLoader) Load(filePath string) (*common.DataSet, error) {

	file, err := common.SafeOpenFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open XML file: %w", err)
	}
	defer file.Close()

	var root XMLNode
	decoder := xml.NewDecoder(file)
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("failed to parse XML: %w", err)
	}

	rowElements, err := findRepeatingElements(&root)
	if err != nil {
		return nil, err
	}

	columnSet := make(map[string]bool)
	for _, row := range rowElements {

		for _, attr := range row.Attrs {
			columnSet[attr.Name.Local] = true
		}

		for _, child := range row.Children {
			if len(child.Children) == 0 && len(child.Attrs) == 0 {
				columnSet[child.XMLName.Local] = true
			}
		}
	}

	columns := make([]string, 0, len(columnSet))
	for col := range columnSet {
		columns = append(columns, col)
	}

	rows := make([]common.DataRow, 0, len(rowElements))
	for _, elem := range rowElements {
		row := make(common.DataRow)

		for _, attr := range elem.Attrs {
			row[attr.Name.Local] = attr.Value
		}

		for _, child := range elem.Children {
			if len(child.Children) == 0 && len(child.Attrs) == 0 {
				row[child.XMLName.Local] = strings.TrimSpace(child.Content)
			}
		}

		rows = append(rows, row)
	}

	return &common.DataSet{
		Columns: columns,
		Rows:    rows,
	}, nil
}

func findRepeatingElements(root *XMLNode) ([]XMLNode, error) {

	elementCounts := make(map[string]int)
	for _, child := range root.Children {
		elementCounts[child.XMLName.Local]++
	}

	var mostRepeatedElement string
	maxCount := 0
	for elem, count := range elementCounts {
		if count > maxCount {
			maxCount = count
			mostRepeatedElement = elem
		}
	}

	if maxCount > 1 {
		var elements []XMLNode
		for _, child := range root.Children {
			if child.XMLName.Local == mostRepeatedElement {
				elements = append(elements, child)
			}
		}
		return elements, nil
	}

	for _, child := range root.Children {
		if len(child.Children) > 1 {
			return findRepeatingElements(&child)
		}
	}

	if len(root.Children) > 0 {
		return root.Children, nil
	}

	return []XMLNode{*root}, nil
}
