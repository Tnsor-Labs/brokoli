package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hc12r/brokolisql-go/internal/processing"
	"github.com/hc12r/brokolisql-go/internal/transformers"
	"github.com/hc12r/brokolisql-go/pkg/common"
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
	// Additional flags
	logLevel      string
	streamingMode bool
	bufferSize    int
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

// Execute runs the root command and handles any errors that occur
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

// getColoredBanner returns a colorful banner
// This is a wrapper that decides which banner style to use
func getColoredBanner() string {
	// Use the box banner for a more modern look
	return GetColoredBoxBanner()
}

func init() {
	// Set up the banner
	coloredBanner := getColoredBanner()
	if coloredBanner != "" {
		rootCmd.SetUsageTemplate(fmt.Sprintf("%s\n\nUsage:{{if .Runnable}}{{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}{{.CommandPath}} [command]{{end}}\n\n{{if gt (len .Aliases) 0}}Aliases:\n  {{.NameAndAliases}}\n\n{{end}}{{if .HasExample}}Examples:\n{{.Example}}\n\n{{end}}{{if .HasAvailableSubCommands}}Available Commands:{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name \"help\"))}}{{\"  \"}}{{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}\n\n{{end}}{{if .HasAvailableLocalFlags}}Flags:\n{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}\n\n{{end}}{{if .HasAvailableInheritedFlags}}Global Flags:\n{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}\n\n{{end}}{{if .HasHelpSubCommands}}Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}{{\"  \"}}{{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}\n\n{{end}}{{if .HasAvailableSubCommands}}Use \"{{.CommandPath}} [command] --help\" for more information about a command.{{end}}\n", coloredBanner))
	}

	// Use Flags() instead of PersistentFlags() for the root command
	flags := rootCmd.Flags()

	// Register all flags
	flags.StringVarP(&inputFile, "input", "i", "", "Input file path (required unless using fetch mode)")
	flags.StringVarP(&outputFile, "output", "o", "", "Output SQL file path (required)")
	flags.StringVarP(&tableName, "table", "t", "", "Table name for SQL statements (required)")
	flags.StringVarP(&format, "format", "f", "", "Input file format (csv, json, xml, xlsx) - if not specified, will be inferred from file extension")
	flags.StringVarP(&dialect, "dialect", "d", "generic", "SQL dialect (generic, postgres, mysql, sqlite, sqlserver, oracle)")
	flags.IntVarP(&batchSize, "batch-size", "b", 100, "Number of rows per INSERT statement")
	flags.BoolVarP(&createTable, "create-table", "c", false, "Generate CREATE TABLE statement")
	flags.StringVarP(&transformFile, "transform", "r", "", "JSON file with transformation rules")
	flags.BoolVarP(&normalizeColumns, "normalize", "n", true, "Normalize column names for SQL compatibility")
	flags.StringVar(&logLevel, "log-level", "info", "Log level (debug, info, warning, error, fatal)")

	// Streaming mode flags
	flags.BoolVar(&streamingMode, "streaming", false, "Enable streaming mode for processing large files with constant memory usage")
	flags.IntVar(&bufferSize, "buffer-size", 1000, "Number of rows to buffer in memory when using streaming mode")

	// Fetch mode flags
	flags.BoolVar(&fetchMode, "fetch", false, "Enable fetch mode to retrieve data from remote sources")
	flags.StringVar(&fetchSource, "source", "", "Source URL or connection string for fetch mode")
	flags.StringVar(&fetchType, "source-type", "rest", "Source type for fetch mode (rest, etc.)")

	// Mark required flags
	_ = rootCmd.MarkFlagRequired("output")
	_ = rootCmd.MarkFlagRequired("table")
}

func runConversion() error {
	// Display colored banner
	coloredBanner := getColoredBanner()
	if coloredBanner != "" {
		fmt.Println(coloredBanner)
	}

	// Set up logger
	logger := common.NewLogger(common.LogLevelFromString(logLevel))
	logger.Info("Starting BrokoliSQL")
	logger.Debug("Log level set to: %s", logLevel)

	var dataset *common.DataSet

	// Validate common required flags
	if outputFile == "" || tableName == "" {
		logger.Fatal("Output and table flags are required")
	}

	// Check if we're in fetch mode, streaming mode, or traditional mode
	if fetchMode {
		// Fetch mode
		// Validate fetch mode parameters
		if fetchSource == "" {
			logger.Fatal("Source URL or connection string is required when using fetch mode")
		}

		logger.Info("Fetch mode enabled, retrieving data from %s using %s fetcher", fetchSource, fetchType)
		logger.Debug("Fetch source: %s, fetch type: %s", fetchSource, fetchType)

		// Get the appropriate fetcher
		fetcher, err := fetchers.GetFetcher(fetchType)
		if err != nil {
			logger.Fatal("Failed to get fetcher: %v", err)
		}

		// Create options map for the fetcher
		options := make(map[string]interface{})
		// Add default options for REST fetcher
		if fetchType == "rest" {
			options["method"] = "GET"
			options["headers"] = map[string]string{
				"Accept": "application/json",
			}
			logger.Debug("REST fetcher options: %v", options)
		}

		// Fetch the data
		logger.Progress("Fetching data from %s...", fetchSource)
		dataset, err = fetcher.Fetch(fetchSource, options)
		if err != nil {
			logger.Fatal("Failed to fetch data: %v", err)
		}

		logger.Info("Successfully fetched %d rows of data", len(dataset.Rows))
		logger.Debug("Fetched column names: %v", dataset.Columns)

		// Apply transformations if specified
		if transformFile != "" {
			logger.Info("Applying transformations from %s", transformFile)
			logger.Debug("Transformation file content will be processed")

			transformEngine, err := transformers.NewTransformEngine(transformFile)
			if err != nil {
				logger.Fatal("Failed to initialize transform engine: %v", err)
			}

			logger.Progress("Applying transformations...")
			if err := transformEngine.ApplyTransformations(dataset); err != nil {
				logger.Fatal("Failed to apply transformations: %v", err)
			}

			logger.Info("Transformations applied successfully, resulting in %d rows", len(dataset.Rows))
			logger.Debug("Transformed column names: %v", dataset.Columns)
		}

		// Generate SQL
		logger.Progress("Generating SQL with dialect: %s...", dialect)
		sqlGenerator, err := processing.NewSQLGenerator(processing.SQLGeneratorOptions{
			Dialect:          dialect,
			TableName:        tableName,
			CreateTable:      createTable,
			BatchSize:        batchSize,
			NormalizeColumns: normalizeColumns,
		})
		if err != nil {
			logger.Fatal("Failed to initialize SQL generator: %v", err)
		}

		logger.Debug("Starting SQL generation for %d rows", len(dataset.Rows))
		sql, err := sqlGenerator.Generate(dataset)
		if err != nil {
			logger.Fatal("Failed to generate SQL: %v", err)
		}
		logger.Debug("SQL generation complete, size: %d bytes", len(sql))

		// Write output
		logger.Progress("Writing SQL to %s...", outputFile)
		if err := common.SafeWriteFile(outputFile, []byte(sql), 0600); err != nil {
			logger.Fatal("Failed to write output file: %v", err)
		}

		logger.Info("Successfully fetched data and saved SQL to %s", outputFile)

	} else {
		// File mode (either streaming or traditional)
		// Validate required flags
		if inputFile == "" {
			logger.Fatal("Input file path is required when not using fetch mode")
		}

		// Determine file format if not specified
		fileFormat := format
		if fileFormat == "" {
			ext := filepath.Ext(inputFile)
			switch ext {
			case ".csv":
				fileFormat = "csv"
			case ".json":
				fileFormat = "json"
			case ".xml":
				fileFormat = "xml"
			case ".xlsx", ".xls":
				fileFormat = "excel"
			default:
				logger.Fatal("Could not determine file format from extension: %s, please specify with --format", ext)
			}
		}

		logger.Info("Processing file: %s (format: %s)", inputFile, fileFormat)

		if streamingMode {
			// Streaming mode
			logger.Info("Streaming mode enabled, processing with constant memory usage")

			// Check if the file format supports streaming
			if fileFormat != "csv" && fileFormat != "json" {
				logger.Fatal("Streaming mode is currently only supported for CSV and JSON files")
			}

			// Create streaming SQL generator
			streamingOptions := processing.StreamingSQLGeneratorOptions{
				SQLGeneratorOptions: processing.SQLGeneratorOptions{
					Dialect:          dialect,
					TableName:        tableName,
					CreateTable:      createTable,
					BatchSize:        batchSize,
					NormalizeColumns: normalizeColumns,
				},
				OutputFile:    outputFile,
				BufferSize:    bufferSize,
				TransformFile: transformFile,
			}

			// Log if transformations will be applied
			if transformFile != "" {
				logger.Info("Applying transformations from %s in streaming mode", transformFile)
			}

			streamingGenerator, err := processing.NewStreamingSQLGenerator(streamingOptions)
			if err != nil {
				logger.Fatal("Failed to initialize streaming SQL generator: %v", err)
			}

			// Process the stream
			logger.Progress("Processing file in streaming mode...")
			err = streamingGenerator.ProcessStream(inputFile)
			if err != nil {
				logger.Fatal("Failed to process stream: %v", err)
			}

			logger.Info("Successfully processed file in streaming mode and saved SQL to %s", outputFile)

		} else {
			// Traditional mode (load everything into memory)
			// Get the appropriate loader
			loader, err := loaders.GetLoader(inputFile)
			if err != nil {
				logger.Fatal("Failed to get loader: %v", err)
			}

			// Load the data
			logger.Progress("Loading data from file...")
			dataset, err = loader.Load(inputFile)
			if err != nil {
				logger.Fatal("Failed to load data: %v", err)
			}

			logger.Info("Loaded %d rows with %d columns", len(dataset.Rows), len(dataset.Columns))
			logger.Debug("Column names: %v", dataset.Columns)

			// Apply transformations if specified
			if transformFile != "" {
				logger.Info("Applying transformations from %s", transformFile)
				logger.Debug("Transformation file content will be processed")

				transformEngine, err := transformers.NewTransformEngine(transformFile)
				if err != nil {
					logger.Fatal("Failed to initialize transform engine: %v", err)
				}

				logger.Progress("Applying transformations...")
				if err := transformEngine.ApplyTransformations(dataset); err != nil {
					logger.Fatal("Failed to apply transformations: %v", err)
				}

				logger.Info("Transformations applied successfully, resulting in %d rows", len(dataset.Rows))
				logger.Debug("Transformed column names: %v", dataset.Columns)
			}

			// Generate SQL
			logger.Progress("Generating SQL with dialect: %s...", dialect)
			sqlGenerator, err := processing.NewSQLGenerator(processing.SQLGeneratorOptions{
				Dialect:          dialect,
				TableName:        tableName,
				CreateTable:      createTable,
				BatchSize:        batchSize,
				NormalizeColumns: normalizeColumns,
			})
			if err != nil {
				logger.Fatal("Failed to initialize SQL generator: %v", err)
			}

			logger.Debug("Starting SQL generation for %d rows", len(dataset.Rows))
			sql, err := sqlGenerator.Generate(dataset)
			if err != nil {
				logger.Fatal("Failed to generate SQL: %v", err)
			}
			logger.Debug("SQL generation complete, size: %d bytes", len(sql))

			// Write output
			logger.Progress("Writing SQL to %s...", outputFile)
			if err := common.SafeWriteFile(outputFile, []byte(sql), 0600); err != nil {
				logger.Fatal("Failed to write output file: %v", err)
			}

			logger.Info("Successfully converted %s to SQL and saved to %s", inputFile, outputFile)
		}
	}

	return nil
}
