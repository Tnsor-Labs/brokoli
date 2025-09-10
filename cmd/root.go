package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hc12r/brokolisql-go/internal/processing"
	"github.com/hc12r/brokolisql-go/internal/transformers"
	"github.com/hc12r/brokolisql-go/pkg/common"
	"github.com/hc12r/brokolisql-go/pkg/errors"
	"github.com/hc12r/brokolisql-go/pkg/fetchers"
	"github.com/hc12r/brokolisql-go/pkg/loaders"

	"github.com/spf13/cobra"
)

var (
	inputFile        string
	outputFile       string
	tableName        string
	format           string
	dialect          string
	batchSize        int
	createTable      bool
	transformFile    string
	normalizeColumns bool
	fetchMode        bool
	fetchSource      string
	fetchType        string
)

var rootCmd = &cobra.Command{
	Use:   "brokolisql",
	Short: "BrokoliSQL converts structured data files to SQL INSERT statements",
	Long: `BrokoliSQL is a command-line tool designed to facilitate the conversion of 
structured data files—such as CSV, Excel, JSON, and XML—into SQL INSERT statements.

It solves common problems faced during data import, transformation, and database 
seeding by offering a flexible, extensible, and easy-to-use interface.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConversion()
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

func init() {
	flags := rootCmd.PersistentFlags()
	flags.StringVar(&inputFile, "input", "", "Input file path (required unless using fetch mode)")
	flags.StringVar(&outputFile, "output", "", "Output SQL file path (required)")
	flags.StringVar(&tableName, "table", "", "Table name for SQL statements (required)")
	flags.StringVar(&format, "format", "", "Input file format (csv, json, xml, xlsx) - if not specified, will be inferred from file extension")
	flags.StringVar(&dialect, "dialect", "generic", "SQL dialect (generic, postgres, mysql, sqlite, sqlserver, oracle)")
	flags.IntVar(&batchSize, "batch-size", 100, "Number of rows per INSERT statement")
	flags.BoolVar(&createTable, "create-table", false, "Generate CREATE TABLE statement")
	flags.StringVar(&transformFile, "transform", "", "JSON file with transformation rules")
	flags.BoolVar(&normalizeColumns, "normalize", true, "Normalize column names for SQL compatibility")

	// Fetch mode flags
	flags.BoolVar(&fetchMode, "fetch", false, "Enable fetch mode to retrieve data from remote sources")
	flags.StringVar(&fetchSource, "source", "", "Source URL or connection string for fetch mode")
	flags.StringVar(&fetchType, "source-type", "rest", "Source type for fetch mode (rest, etc.)")

	flags.StringVarP(&inputFile, "i", "i", "", "Input file path (shorthand)")
	flags.StringVarP(&outputFile, "o", "o", "", "Output SQL file path (shorthand)")
	flags.StringVarP(&tableName, "t", "t", "", "Table name for SQL statements (shorthand)")
	flags.StringVarP(&format, "f", "f", "", "Input file format (shorthand)")
	flags.StringVarP(&dialect, "d", "d", "generic", "SQL dialect (shorthand)")
	flags.IntVarP(&batchSize, "b", "b", 100, "Number of rows per INSERT statement (shorthand)")
	flags.BoolVarP(&createTable, "c", "c", false, "Generate CREATE TABLE statement (shorthand)")
	flags.StringVarP(&transformFile, "r", "r", "", "JSON file with transformation rules (shorthand)")
	flags.BoolVarP(&normalizeColumns, "n", "n", true, "Normalize column names for SQL compatibility (shorthand)")

	// Only mark input as required if not in fetch mode
	errors.CheckError(rootCmd.MarkFlagRequired("output"))
	errors.CheckError(rootCmd.MarkFlagRequired("table"))

}

func runConversion() error {
	var dataset *common.DataSet
	var err error

	// Check if we're in fetch mode or file mode
	if fetchMode {
		// Validate fetch mode parameters
		if fetchSource == "" {
			return fmt.Errorf("source URL or connection string is required when using fetch mode")
		}

		// Get the appropriate fetcher
		fetcher, err := fetchers.GetFetcher(fetchType)
		if err != nil {
			return fmt.Errorf("failed to get fetcher: %w", err)
		}

		// Create options map for the fetcher
		options := make(map[string]interface{})
		// Add default options for REST fetcher
		if fetchType == "rest" {
			options["method"] = "GET"
			options["headers"] = map[string]string{
				"Accept": "application/json",
			}
		}

		// Fetch the data
		fmt.Printf("Fetching data from %s using %s fetcher...\n", fetchSource, fetchType)
		dataset, err = fetcher.Fetch(fetchSource, options)
		if err != nil {
			return fmt.Errorf("failed to fetch data: %w", err)
		}
		fmt.Printf("Successfully fetched %d rows of data\n", len(dataset.Rows))
	} else {
		// Traditional file loading mode
		if inputFile == "" {
			return fmt.Errorf("input file is required when not using fetch mode")
		}

		if format == "" {
			ext := filepath.Ext(inputFile)
			switch ext {
			case ".csv":
				format = "csv"
			case ".json":
				format = "json"
			case ".xml":
				format = "xml"
			case ".xlsx", ".xls":
				format = "excel"
			default:
				return fmt.Errorf("could not determine file format from extension: %s, please specify with --format", ext)
			}
		}

		loader, err := loaders.GetLoader(inputFile)
		if err != nil {
			return fmt.Errorf("failed to get loader: %w", err)
		}

		dataset, err = loader.Load(inputFile)
		if err != nil {
			return fmt.Errorf("failed to load data: %w", err)
		}
	}

	if transformFile != "" {
		transformEngine, err := transformers.NewTransformEngine(transformFile)
		if err != nil {
			return fmt.Errorf("failed to initialize transform engine: %w", err)
		}

		if err := transformEngine.ApplyTransformations(dataset); err != nil {
			return fmt.Errorf("failed to apply transformations: %w", err)
		}
	}

	sqlGenerator, err := processing.NewSQLGenerator(processing.SQLGeneratorOptions{
		Dialect:          dialect,
		TableName:        tableName,
		CreateTable:      createTable,
		BatchSize:        batchSize,
		NormalizeColumns: normalizeColumns,
	})
	if err != nil {
		return fmt.Errorf("failed to initialize SQL generator: %w", err)
	}

	sql, err := sqlGenerator.Generate(dataset)
	if err != nil {
		return fmt.Errorf("failed to generate SQL: %w", err)
	}

	if err := common.SafeWriteFile(outputFile, []byte(sql), 0600); err != nil {
		return fmt.Errorf("failed to write output file: %w", err)
	}

	fmt.Printf("Successfully converted %s to SQL and saved to %s\n", inputFile, outputFile)
	return nil
}
